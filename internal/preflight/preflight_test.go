package preflight

import (
	"testing"

	"github.com/paulojauregui/mealie-feeder/internal/config"
)

func TestExtractJSONLD_Variants(t *testing.T) {
	t.Parallel()

	t.Run("direct recipe jsonld", func(t *testing.T) {
		t.Parallel()
		html := `
<!DOCTYPE html>
<html>
<head>
<script type="application/ld+json">
{
  "@context": "https://schema.org",
  "@type": "Recipe",
  "name": "Classic Chocolate Chip Cookies",
  "recipeCategory": "Dessert",
  "recipeCuisine": "American",
  "url": "https://example.com/cookies",
  "recipeIngredient": [
    "2 cups flour",
    "1 cup chocolate chips"
  ],
  "recipeInstructions": [
    "Preheat oven to 375F.",
    "Mix ingredients and bake for 10 minutes."
  ]
}
</script>
</head>
<body></body>
</html>`

		res := InspectHTML([]byte(html), config.SchemaModeJSONLD)
		if res.Verdict != VerdictValid {
			t.Fatalf("Verdict = %q, want %q", res.Verdict, VerdictValid)
		}
		if res.Title != "Classic Chocolate Chip Cookies" {
			t.Errorf("Title = %q", res.Title)
		}
		if res.Category != "Dessert" {
			t.Errorf("Category = %q", res.Category)
		}
		if res.IngredientCount != 2 || res.InstructionCount != 2 {
			t.Errorf("Counts: ing=%d, ins=%d", res.IngredientCount, res.InstructionCount)
		}
	})

	t.Run("@graph nested recipe", func(t *testing.T) {
		t.Parallel()
		html := `
<html>
<head>
<script type="application/ld+json; charset=utf-8">
{
  "@context": "https://schema.org",
  "@graph": [
    {
      "@type": "WebSite",
      "name": "Food Site"
    },
    {
      "@type": ["Recipe", "Article"],
      "name": "Spaghetti Carbonara &amp; Pancetta",
      "recipeIngredient": ["200g spaghetti", "100g pancetta", "2 eggs"],
      "recipeInstructions": [
        {
          "@type": "HowToStep",
          "text": "Boil pasta in salted water."
        },
        {
          "@type": "HowToStep",
          "text": "Fry pancetta until crisp."
        }
      ]
    }
  ]
}
</script>
</head>
</html>`

		res := InspectHTML([]byte(html), config.SchemaModeJSONLD)
		if res.Verdict != VerdictValid {
			t.Fatalf("Verdict = %q, want %q", res.Verdict, VerdictValid)
		}
		if res.Title != "Spaghetti Carbonara & Pancetta" {
			t.Errorf("Title = %q (unescaped)", res.Title)
		}
		if res.IngredientCount != 3 || res.InstructionCount != 2 {
			t.Errorf("Counts: ing=%d, ins=%d", res.IngredientCount, res.InstructionCount)
		}
	})

	t.Run("HowToSection nested steps", func(t *testing.T) {
		t.Parallel()
		html := `
<script type="application/ld+json">
{
  "@context": "https://schema.org",
  "@type": "Recipe",
  "name": "Artisan Sourdough Bread",
  "recipeIngredient": ["500g flour", "350g water", "100g starter", "10g salt"],
  "recipeInstructions": [
    {
      "@type": "HowToSection",
      "name": "Dough Prep",
      "itemListElement": [
        {"@type": "HowToStep", "text": "Mix flour and water."},
        {"@type": "HowToStep", "text": "Add starter and salt."}
      ]
    },
    {
      "@type": "HowToSection",
      "name": "Bake",
      "itemListElement": [
        {"@type": "HowToStep", "text": "Bake at 450F for 30 minutes in Dutch oven."}
      ]
    }
  ]
}
</script>`

		res := InspectHTML([]byte(html), config.SchemaModeJSONLD)
		if res.Verdict != VerdictValid {
			t.Fatalf("Verdict = %q, want %q", res.Verdict, VerdictValid)
		}
		if res.InstructionCount != 3 {
			t.Errorf("InstructionCount = %d, want 3", res.InstructionCount)
		}
	})

	t.Run("malformed json block before valid block", func(t *testing.T) {
		t.Parallel()
		html := `
<script type="application/ld+json">
{ invalid json syntax here...
</script>
<script type="application/ld+json">
{
  "@context": "https://schema.org",
  "@type": "Recipe",
  "name": "Valid Recipe After Broken Block",
  "recipeIngredient": ["salt"],
  "recipeInstructions": "Just add salt."
}
</script>`

		res := InspectHTML([]byte(html), config.SchemaModeJSONLD)
		if res.Verdict != VerdictValid {
			t.Fatalf("Verdict = %q, want %q", res.Verdict, VerdictValid)
		}
		if res.Title != "Valid Recipe After Broken Block" {
			t.Errorf("Title = %q", res.Title)
		}
	})
}

func TestInspectHTML_EmptyRecipeAndNonRecipe(t *testing.T) {
	t.Parallel()

	t.Run("empty recipe schema missing ingredients", func(t *testing.T) {
		t.Parallel()
		html := `
<script type="application/ld+json">
{
  "@type": "Recipe",
  "name": "Empty Recipe",
  "recipeInstructions": ["Step 1"]
}
</script>`

		res := InspectHTML([]byte(html), config.SchemaModeJSONLD)
		if res.Verdict != VerdictEmptyRecipe {
			t.Errorf("Verdict = %q, want %q", res.Verdict, VerdictEmptyRecipe)
		}
	})

	t.Run("non-recipe gear review", func(t *testing.T) {
		t.Parallel()
		html := `
<html>
<head><title>Best Chef Knives of 2026</title></head>
<body>
<h1>Top 10 Chef Knives</h1>
<p>Here are our favorite kitchen knives.</p>
</body>
</html>`

		res := InspectHTML([]byte(html), config.SchemaModeJSONLD)
		if res.Verdict != VerdictNonRecipe {
			t.Errorf("Verdict = %q, want %q", res.Verdict, VerdictNonRecipe)
		}
	})
}

func TestInspectHTML_Microdata(t *testing.T) {
	t.Parallel()

	microdataHTML := `
<div itemscope itemtype="http://schema.org/Recipe">
  <h1 itemprop="name">Rigatoni Alla Vodka</h1>
  <span itemprop="recipeCategory">Dinner</span>
  <ul>
    <li itemprop="recipeIngredient">1 lb rigatoni</li>
    <li itemprop="recipeIngredient">1/2 cup vodka</li>
    <li itemprop="recipeIngredient">1 cup heavy cream</li>
  </ul>
  <div itemprop="recipeInstructions">
    Boil pasta, simmer sauce with vodka and cream, toss together.
  </div>
</div>`

	// Under JSON-LD mode, microdata is ignored -> non_recipe
	resJSONLD := InspectHTML([]byte(microdataHTML), config.SchemaModeJSONLD)
	if resJSONLD.Verdict != VerdictNonRecipe {
		t.Errorf("JSON-LD mode: Verdict = %q, want %q", resJSONLD.Verdict, VerdictNonRecipe)
	}

	// Under Microdata mode -> valid
	resMicro := InspectHTML([]byte(microdataHTML), config.SchemaModeMicrodata)
	if resMicro.Verdict != VerdictValid {
		t.Errorf("Microdata mode: Verdict = %q, want %q", resMicro.Verdict, VerdictValid)
	}
	if resMicro.Title != "Rigatoni Alla Vodka" {
		t.Errorf("Title = %q", resMicro.Title)
	}
	if resMicro.IngredientCount != 3 || resMicro.InstructionCount != 1 {
		t.Errorf("Counts: ing=%d, ins=%d", resMicro.IngredientCount, resMicro.InstructionCount)
	}

	// Under Auto mode -> valid
	resAuto := InspectHTML([]byte(microdataHTML), config.SchemaModeAuto)
	if resAuto.Verdict != VerdictValid {
		t.Errorf("Auto mode: Verdict = %q, want %q", resAuto.Verdict, VerdictValid)
	}
}

func TestInspectHTML_PaywallHeuristic(t *testing.T) {
	t.Parallel()

	paywallHTML := `
<html>
<head><title>Subscriber Exclusive Article</title></head>
<body>
<div class="paywall">
  <h2>Subscribe to continue reading</h2>
  <p>This content is for subscribers only.</p>
</div>
</body>
</html>`

	res := InspectHTML([]byte(paywallHTML), config.SchemaModeJSONLD)
	if res.Verdict != VerdictPaywall {
		t.Errorf("Verdict = %q, want %q", res.Verdict, VerdictPaywall)
	}

	// If valid recipe schema is present despite paywall banner, recipe wins
	recipeWithBanner := `
<script type="application/ld+json">
{
  "@type": "Recipe",
  "name": "Secret Recipe",
  "recipeIngredient": ["ingredient 1"],
  "recipeInstructions": ["step 1"]
}
</script>
<div>Subscribe to continue for more perks</div>`

	resWinner := InspectHTML([]byte(recipeWithBanner), config.SchemaModeJSONLD)
	if resWinner.Verdict != VerdictValid {
		t.Errorf("Recipe with banner: Verdict = %q, want %q", resWinner.Verdict, VerdictValid)
	}
}

func TestIsOpenGraphStub(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		ingredients  []string
		instructions []string
		description  string
		wantStub     bool
	}{
		{
			name:         "detected stub string in description",
			ingredients:  []string{"flour"},
			instructions: []string{"bake"},
			description:  "We could not detect ingredients for this recipe automatically.",
			wantStub:     true,
		},
		{
			name:         "both ingredients and instructions empty",
			ingredients:  nil,
			instructions: nil,
			description:  "A lovely chocolate cake.",
			wantStub:     true,
		},
		{
			name:         "ingredients empty but instructions populated (0-ingredient bug)",
			ingredients:  []string{},
			instructions: []string{"Step 1", "Step 2"},
			description:  "Delicious stew.",
			wantStub:     true,
		},
		{
			name:         "instructions empty but ingredients populated (0-step bug)",
			ingredients:  []string{"1 cup sugar"},
			instructions: []string{},
			description:  "Sweet dessert.",
			wantStub:     true,
		},
		{
			name:         "complete valid recipe",
			ingredients:  []string{"flour", "sugar", "butter"},
			instructions: []string{"Mix and bake."},
			description:  "Classic shortbread cookies.",
			wantStub:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsOpenGraphStub(tc.ingredients, tc.instructions, tc.description)
			if got != tc.wantStub {
				t.Errorf("IsOpenGraphStub = %v, want %v", got, tc.wantStub)
			}
		})
	}
}
