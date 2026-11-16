# M08 Specification: Schema.org JSON-LD & Microdata Preflight Validator

## 1. Objective & Target Files

Implement zero-regex HTML tokenization (`golang.org/x/net/html`) in `internal/preflight` to validate Schema.org `Recipe` JSON-LD and Microdata locally before calling Mealie, plus post-ingest OpenGraph stub detection.

**Target Files (strict scope):**
1. `internal/preflight/jsonld.go`
2. `internal/preflight/preflight.go`
3. `internal/preflight/preflight_test.go`

---

## 2. Package Contract (`package preflight`)

### 2.1 JSON-LD & Microdata Extraction (`internal/preflight/jsonld.go`)

```go
package preflight

// ExtractedRecipe holds normalized Schema.org Recipe fields extracted from HTML.
// Pre-conditions: Populated from JSON-LD (<script type="application/ld+json">) or Microdata (itemtype="http(s)://schema.org/Recipe").
// Post-conditions: Title and Category are HTML-unescaped and whitespace-trimmed.
type ExtractedRecipe struct {
	SchemaKind       string // "jsonld" | "microdata"
	Title            string
	Category         string
	Cuisine          string
	CanonicalURL     string
	Ingredients      []string
	Instructions     []string
}

// ExtractJSONLD tokenizes rawHTML using golang.org/x/net/html (no regex HTML matching) and extracts the first valid Schema.org Recipe node.
// Pre-conditions: rawHTML may contain multiple <script type="application/ld+json"> blocks, top-level arrays, or @graph containers.
// Post-conditions: Matches @type == "Recipe" or @type slice containing "Recipe" (or schema.org URI variants); ignores malformed JSON blocks cleanly without panicking.
func ExtractJSONLD(rawHTML []byte) (*ExtractedRecipe, bool)

// ExtractMicrodata tokenizes rawHTML using golang.org/x/net/html to detect Schema.org Recipe Microdata (e.g., smittenkitchen.com).
// Pre-conditions: rawHTML may contain nested itemscope/itemtype attributes.
// Post-conditions: Recognizes itemtype containing "http://schema.org/Recipe" or "https://schema.org/Recipe" and collects itemprop="name", "recipeIngredient"/"ingredients", and "recipeInstructions".
func ExtractMicrodata(rawHTML []byte) (*ExtractedRecipe, bool)
```

### 2.2 Preflight Verdict & OpenGraph Stub Guard (`internal/preflight/preflight.go`)

```go
import "github.com/paulojauregui/mealie-feeder/internal/config"

// Verdict classifies whether an HTML document is safe to send to Mealie create/html-or-json.
// Pre-conditions: Equal to one of the four constants below.
// Post-conditions: Determines ledger state transition in internal/feeder.
type Verdict string

const (
	VerdictValid       Verdict = "valid"
	VerdictNonRecipe   Verdict = "non_recipe"
	VerdictEmptyRecipe Verdict = "empty_recipe"
	VerdictPaywall     Verdict = "paywall"
)

// Result summarizes preflight inspection metrics and extracted metadata.
// Pre-conditions: Produced by InspectHTML.
// Post-conditions: Verdict == VerdictValid iff a matching schema contains >= 1 non-empty ingredient AND >= 1 non-empty instruction.
type Result struct {
	Verdict          Verdict
	SchemaKind       string
	Title            string
	Category         string
	CanonicalURL     string
	IngredientCount  int
	InstructionCount int
	Reason           string
}

// InspectHTML evaluates rawHTML against schemaMode (jsonld, microdata, or auto), checking recipe schema completeness and paywall markers.
// Pre-conditions: schemaMode defaults to config.SchemaModeJSONLD when empty.
// Post-conditions: Rejects roundups, gear reviews, empty schemas, and paywalled pages deterministically without calling external services.
func InspectHTML(rawHTML []byte, schemaMode config.SchemaMode) Result

// IsOpenGraphStub detects Mealie RecipeScraperOpenGraph fallback stubs after ingestion.
// Pre-conditions: Arguments come from Mealie GET /api/recipes/{slug}.
// Post-conditions: Returns true if description contains "Could not detect ingredients" (case-insensitive) OR both ingredients and instructions are empty.
func IsOpenGraphStub(ingredients []string, instructions []string, description string) bool
```

---

## 3. Parsing & Edge-Case Invariants

1. **Media Type Normalization:** `<script type="application/ld+json; charset=utf-8">` and uppercase attributes (`TYPE="APPLICATION/LD+JSON"`) must match cleanly via `mime.ParseMediaType`.
2. **Instruction Structures:** `recipeInstructions` in Schema.org JSON-LD appears in 4 real-world variants that must all be extracted cleanly:
   - Single string (`"Mix and bake."`)
   - String slice (`["Preheat oven.", "Bake 20 min."]`)
   - Slice of `HowToStep` objects (`[{"@type": "HowToStep", "text": "..."}]`)
   - Slice of `HowToSection` objects containing `itemListElement` slices of `HowToStep` (`[{"@type": "HowToSection", "itemListElement": [...]}]`)
3. **HTML Entity & Whitespace Cleanup:** Decode `&amp;`, `&#39;`, `&rsquo;`, `&nbsp;` via `html.UnescapeString` and normalize internal control whitespace.
4. **Paywall Heuristic:** Only classify as `VerdictPaywall` when NO valid recipe schema exists AND HTML contains strong paywall indicators (e.g., `"subscribe to continue"`, `"subscribers only"`, `data-paywall`, `"paywall"` meta/class markers). If a valid Recipe JSON-LD with ingredients & instructions is present, `VerdictValid` wins.

---

## 4. Test Plan (`internal/preflight/preflight_test.go`)

1. Direct JSON-LD `Recipe`, `@graph` nested `Recipe` (Yoast/WPRM pattern), top-level JSON array, `@type: ["Recipe", "NewsArticle"]`, and `HowToSection` nested steps.
2. Malformed JSON block before a valid second `<script type="application/ld+json">` block succeeds cleanly.
3. Missing `recipeIngredient` or empty `recipeInstructions` returns `VerdictEmptyRecipe`.
4. Roundup / gear review HTML lacking `Recipe` schema returns `VerdictNonRecipe`.
5. Microdata HTML fixture (modeling `smittenkitchen.com`) returns `VerdictNonRecipe` under `SchemaModeJSONLD` and `VerdictValid` under `SchemaModeMicrodata` and `SchemaModeAuto`.
6. Paywall HTML without recipe schema returns `VerdictPaywall`.
7. `IsOpenGraphStub` detects `"Could not detect ingredients"` and empty ingredient/instruction recipes while passing normal recipes.

---

## 5. Verification Command

```bash
go test -v -race -cover ./internal/preflight
```
