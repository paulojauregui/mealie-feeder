package preflight

import (
	"bytes"
	"strings"

	"github.com/paulojauregui/mealie-feeder/internal/config"
)

// Verdict classifies whether an HTML document is safe to send to Mealie create/html-or-json.
// Pre-conditions: Equal to one of the four constants below.
// Post-conditions: Determines ledger state transition in internal/feeder.
type Verdict string

const (
	// VerdictValid indicates complete Recipe schema with >= 1 ingredient and >= 1 instruction.
	VerdictValid Verdict = "valid"
	// VerdictNonRecipe indicates document lacks Schema.org Recipe markup (e.g. roundup, review).
	VerdictNonRecipe Verdict = "non_recipe"
	// VerdictEmptyRecipe indicates Recipe markup was found but has 0 ingredients or 0 instructions.
	VerdictEmptyRecipe Verdict = "empty_recipe"
	// VerdictPaywall indicates document presents subscriber login wall or paywall gates without valid recipe.
	VerdictPaywall Verdict = "paywall"
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
func InspectHTML(rawHTML []byte, schemaMode config.SchemaMode) Result {
	mode := schemaMode
	if mode == "" {
		mode = config.SchemaModeJSONLD
	}

	var rec *ExtractedRecipe
	var ok bool

	switch mode {
	case config.SchemaModeMicrodata:
		rec, ok = ExtractMicrodata(rawHTML)
	case config.SchemaModeAuto:
		rec, ok = ExtractJSONLD(rawHTML)
		if !ok {
			rec, ok = ExtractMicrodata(rawHTML)
		}
	case config.SchemaModeJSONLD:
		fallthrough
	default:
		rec, ok = ExtractJSONLD(rawHTML)
	}

	if ok && rec != nil {
		ingCount := len(rec.Ingredients)
		insCount := len(rec.Instructions)

		if ingCount >= 1 && insCount >= 1 {
			return Result{
				Verdict:          VerdictValid,
				SchemaKind:       rec.SchemaKind,
				Title:            rec.Title,
				Category:         rec.Category,
				CanonicalURL:     rec.CanonicalURL,
				IngredientCount:  ingCount,
				InstructionCount: insCount,
			}
		}

		return Result{
			Verdict:          VerdictEmptyRecipe,
			SchemaKind:       rec.SchemaKind,
			Title:            rec.Title,
			IngredientCount:  ingCount,
			InstructionCount: insCount,
			Reason:           "recipe markup found but missing ingredients or instructions",
		}
	}

	// No Recipe markup found; check for paywall markers
	if hasPaywallMarkers(rawHTML) {
		return Result{
			Verdict: VerdictPaywall,
			Reason:  "subscriber paywall or login gate detected without recipe schema",
		}
	}

	return Result{
		Verdict: VerdictNonRecipe,
		Reason:  "no Schema.org Recipe markup found",
	}
}

// hasPaywallMarkers inspects HTML for strong paywall or subscriber-only gate indicators.
func hasPaywallMarkers(rawHTML []byte) bool {
	lower := bytes.ToLower(rawHTML)
	markers := [][]byte{
		[]byte("subscribe to continue"),
		[]byte("subscribers only"),
		[]byte("data-paywall"),
		[]byte(`class="paywall"`),
		[]byte(`id="paywall"`),
		[]byte("subscriber-only"),
		[]byte("already a subscriber"),
		[]byte("join now to read"),
		[]byte("sign in to continue reading"),
	}

	for _, marker := range markers {
		if bytes.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// IsOpenGraphStub detects Mealie RecipeScraperOpenGraph fallback stubs and incomplete scraped recipes after ingestion.
// Pre-conditions: Arguments come from Mealie GET /api/recipes/{slug}.
// Post-conditions: Returns true if description contains "could not detect ingredients" (case-insensitive) OR len(ingredients) == 0 || len(instructions) == 0.
func IsOpenGraphStub(ingredients []string, instructions []string, description string) bool {
	desc := strings.ToLower(description)
	if strings.Contains(desc, "could not detect ingredients") {
		return true
	}
	if len(ingredients) == 0 || len(instructions) == 0 {
		return true
	}
	return false
}
