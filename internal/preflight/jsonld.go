package preflight

import (
	"bytes"
	"encoding/json"
	"html"
	"mime"
	"strings"

	xhtml "golang.org/x/net/html"
)

// ExtractedRecipe holds normalized Schema.org Recipe fields extracted from HTML.
// Pre-conditions: Populated from JSON-LD (<script type="application/ld+json">) or Microdata (itemtype="http(s)://schema.org/Recipe").
// Post-conditions: Title and Category are HTML-unescaped and whitespace-trimmed.
type ExtractedRecipe struct {
	SchemaKind   string // "jsonld" | "microdata"
	Title        string
	Category     string
	Cuisine      string
	CanonicalURL string
	Ingredients  []string
	Instructions []string
}

// ExtractJSONLD tokenizes rawHTML using golang.org/x/net/html (no regex HTML matching) and extracts the first valid Schema.org Recipe node.
// Pre-conditions: rawHTML may contain multiple <script type="application/ld+json"> blocks, top-level arrays, or @graph containers.
// Post-conditions: Matches @type == "Recipe" or @type slice containing "Recipe" (or schema.org URI variants); ignores malformed JSON blocks cleanly without panicking.
func ExtractJSONLD(rawHTML []byte) (*ExtractedRecipe, bool) {
	scripts := extractScriptBlocks(rawHTML)
	for _, scriptContent := range scripts {
		var root any
		if err := json.Unmarshal([]byte(scriptContent), &root); err != nil {
			continue // Skip malformed JSON blocks cleanly
		}

		if recipeNode := findRecipeNode(root); recipeNode != nil {
			rec := parseRecipeNode(recipeNode)
			if rec != nil {
				rec.SchemaKind = "jsonld"
				return rec, true
			}
		}
	}
	return nil, false
}

// extractScriptBlocks extracts raw content from <script type="application/ld+json"> blocks.
func extractScriptBlocks(rawHTML []byte) []string {
	var scripts []string
	tokenizer := xhtml.NewTokenizer(bytes.NewReader(rawHTML))

	for {
		tt := tokenizer.Next()
		if tt == xhtml.ErrorToken {
			break
		}

		if tt == xhtml.StartTagToken {
			tok := tokenizer.Token()
			if strings.EqualFold(tok.Data, "script") {
				isJSONLD := false
				for _, attr := range tok.Attr {
					if strings.EqualFold(attr.Key, "type") {
						medType, _, err := mime.ParseMediaType(attr.Val)
						if err == nil && strings.EqualFold(medType, "application/ld+json") {
							isJSONLD = true
							break
						}
					}
				}

				if isJSONLD {
					if nextTT := tokenizer.Next(); nextTT == xhtml.TextToken {
						content := string(tokenizer.Text())
						scripts = append(scripts, content)
					}
				}
			}
		}
	}
	return scripts
}

// findRecipeNode traverses JSON-LD structures to find the first Recipe node.
func findRecipeNode(node any) map[string]any {
	switch val := node.(type) {
	case map[string]any:
		if isRecipeType(val["@type"]) {
			return val
		}
		if graph, ok := val["@graph"].([]any); ok {
			for _, item := range graph {
				if m, ok := item.(map[string]any); ok {
					if isRecipeType(m["@type"]) {
						return m
					}
				}
			}
		}
		// Search nested values
		for _, v := range val {
			if res := findRecipeNode(v); res != nil {
				return res
			}
		}

	case []any:
		for _, item := range val {
			if res := findRecipeNode(item); res != nil {
				return res
			}
		}
	}
	return nil
}

// isRecipeType checks if an @type value is or contains "Recipe".
func isRecipeType(typeVal any) bool {
	if typeVal == nil {
		return false
	}
	match := func(s string) bool {
		lower := strings.ToLower(strings.TrimSpace(s))
		return lower == "recipe" ||
			lower == "https://schema.org/recipe" ||
			lower == "http://schema.org/recipe" ||
			strings.HasSuffix(lower, "/recipe")
	}

	switch v := typeVal.(type) {
	case string:
		return match(v)
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && match(s) {
				return true
			}
		}
	}
	return false
}

// parseRecipeNode extracts fields from a JSON-LD Recipe object.
func parseRecipeNode(node map[string]any) *ExtractedRecipe {
	rec := &ExtractedRecipe{}

	// Title / Name
	if name, ok := node["name"].(string); ok {
		rec.Title = strings.TrimSpace(html.UnescapeString(name))
	}

	// Category
	rec.Category = parseStringOrArray(node["recipeCategory"])

	// Cuisine
	rec.Cuisine = parseStringOrArray(node["recipeCuisine"])

	// Canonical URL
	if u, ok := node["url"].(string); ok {
		rec.CanonicalURL = strings.TrimSpace(u)
	} else if pageObj, ok := node["mainEntityOfPage"].(map[string]any); ok {
		if u, ok := pageObj["@id"].(string); ok {
			rec.CanonicalURL = strings.TrimSpace(u)
		}
	}

	// Ingredients
	rec.Ingredients = extractIngredients(node)

	// Instructions
	rec.Instructions = extractInstructions(node["recipeInstructions"])

	return rec
}

func parseStringOrArray(val any) string {
	switch v := val.(type) {
	case string:
		return strings.TrimSpace(html.UnescapeString(v))
	case []any:
		var items []string
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				items = append(items, strings.TrimSpace(html.UnescapeString(s)))
			}
		}
		return strings.Join(items, ", ")
	}
	return ""
}

func extractIngredients(node map[string]any) []string {
	var results []string
	raw := node["recipeIngredient"]
	if raw == nil {
		raw = node["ingredients"]
	}

	switch v := raw.(type) {
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				cleaned := strings.TrimSpace(html.UnescapeString(s))
				if cleaned != "" {
					results = append(results, cleaned)
				}
			}
		}
	case string:
		cleaned := strings.TrimSpace(html.UnescapeString(v))
		if cleaned != "" {
			results = append(results, cleaned)
		}
	}
	return results
}

func extractInstructions(raw any) []string {
	var steps []string
	if raw == nil {
		return steps
	}

	switch v := raw.(type) {
	case string:
		cleaned := strings.TrimSpace(html.UnescapeString(v))
		if cleaned != "" {
			steps = append(steps, cleaned)
		}

	case []any:
		for _, item := range v {
			switch elem := item.(type) {
			case string:
				cleaned := strings.TrimSpace(html.UnescapeString(elem))
				if cleaned != "" {
					steps = append(steps, cleaned)
				}
			case map[string]any:
				// HowToStep or HowToSection
				if isType(elem["@type"], "HowToStep") {
					if text, ok := elem["text"].(string); ok {
						cleaned := strings.TrimSpace(html.UnescapeString(text))
						if cleaned != "" {
							steps = append(steps, cleaned)
						}
					}
				} else if isType(elem["@type"], "HowToSection") {
					if list, ok := elem["itemListElement"].([]any); ok {
						for _, sub := range list {
							if subMap, ok := sub.(map[string]any); ok {
								if text, ok := subMap["text"].(string); ok {
									cleaned := strings.TrimSpace(html.UnescapeString(text))
									if cleaned != "" {
										steps = append(steps, cleaned)
									}
								}
							}
						}
					}
				}
			}
		}
	}
	return steps
}

func isType(val any, target string) bool {
	if val == nil {
		return false
	}
	tLower := strings.ToLower(target)
	match := func(s string) bool {
		lower := strings.ToLower(strings.TrimSpace(s))
		return lower == tLower || strings.HasSuffix(lower, "/"+tLower)
	}
	switch v := val.(type) {
	case string:
		return match(v)
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && match(s) {
				return true
			}
		}
	}
	return false
}

// ExtractMicrodata tokenizes rawHTML using golang.org/x/net/html to detect Schema.org Recipe Microdata (e.g., smittenkitchen.com).
// Pre-conditions: rawHTML may contain nested itemscope/itemtype attributes.
// Post-conditions: Recognizes itemtype containing "http://schema.org/Recipe" or "https://schema.org/Recipe" and collects itemprop="name", "recipeIngredient"/"ingredients", and "recipeInstructions".
func ExtractMicrodata(rawHTML []byte) (*ExtractedRecipe, bool) {
	doc, err := xhtml.Parse(bytes.NewReader(rawHTML))
	if err != nil {
		return nil, false
	}

	recipeNode := findMicrodataRecipeNode(doc)
	if recipeNode == nil {
		return nil, false
	}

	rec := &ExtractedRecipe{
		SchemaKind: "microdata",
	}

	collectMicrodataProps(recipeNode, rec)

	if len(rec.Ingredients) > 0 || len(rec.Instructions) > 0 || rec.Title != "" {
		return rec, true
	}
	return nil, false
}

func findMicrodataRecipeNode(n *xhtml.Node) *xhtml.Node {
	if n.Type == xhtml.ElementNode {
		for _, attr := range n.Attr {
			if strings.EqualFold(attr.Key, "itemtype") {
				lower := strings.ToLower(attr.Val)
				if strings.Contains(lower, "schema.org/recipe") {
					return n
				}
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if res := findMicrodataRecipeNode(c); res != nil {
			return res
		}
	}
	return nil
}

func collectMicrodataProps(n *xhtml.Node, rec *ExtractedRecipe) {
	if n.Type == xhtml.ElementNode {
		for _, attr := range n.Attr {
			if strings.EqualFold(attr.Key, "itemprop") {
				propName := strings.ToLower(strings.TrimSpace(attr.Val))
				textVal := getElementText(n)

				switch propName {
				case "name":
					if rec.Title == "" {
						rec.Title = strings.TrimSpace(html.UnescapeString(textVal))
					}
				case "recipeingredient", "ingredients":
					cleaned := strings.TrimSpace(html.UnescapeString(textVal))
					if cleaned != "" {
						rec.Ingredients = append(rec.Ingredients, cleaned)
					}
				case "recipeinstructions":
					cleaned := strings.TrimSpace(html.UnescapeString(textVal))
					if cleaned != "" {
						rec.Instructions = append(rec.Instructions, cleaned)
					}
				case "recipecategory":
					if rec.Category == "" {
						rec.Category = strings.TrimSpace(html.UnescapeString(textVal))
					}
				case "recipecuisine":
					if rec.Cuisine == "" {
						rec.Cuisine = strings.TrimSpace(html.UnescapeString(textVal))
					}
				}
			}
		}
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		collectMicrodataProps(c, rec)
	}
}

func getElementText(n *xhtml.Node) string {
	// First check content attribute
	for _, attr := range n.Attr {
		if strings.EqualFold(attr.Key, "content") && strings.TrimSpace(attr.Val) != "" {
			return attr.Val
		}
	}

	var buf bytes.Buffer
	var extract func(*xhtml.Node)
	extract = func(node *xhtml.Node) {
		if node.Type == xhtml.TextNode {
			buf.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			extract(c)
		}
	}
	extract(n)
	return buf.String()
}
