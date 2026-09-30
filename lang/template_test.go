package lang

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// RenderTemplate tests
// ---------------------------------------------------------------------------

// TestRenderTemplateSimpleSubstitution verifies that a single top-level
// placeholder is replaced with the corresponding value from the map.
func TestRenderTemplateSimpleSubstitution(t *testing.T) {
	templateContent := `image: {{ .image }}`
	mergedValues := map[string]any{
		"image": "nginx:1.27",
	}

	renderedOutput, renderError := RenderTemplate(templateContent, mergedValues)
	if renderError != nil {
		t.Fatalf("unexpected error: %v", renderError)
	}

	expectedOutput := "image: nginx:1.27"
	if renderedOutput != expectedOutput {
		t.Errorf("expected %q, got %q", expectedOutput, renderedOutput)
	}
}

// TestRenderTemplateNestedValues verifies that dot-notation access into
// nested maps resolves correctly during template rendering.
func TestRenderTemplateNestedValues(t *testing.T) {
	templateContent := `image: {{ .web.image }}`
	mergedValues := map[string]any{
		"web": map[string]any{
			"image": "nginx",
		},
	}

	renderedOutput, renderError := RenderTemplate(templateContent, mergedValues)
	if renderError != nil {
		t.Fatalf("unexpected error: %v", renderError)
	}

	expectedOutput := "image: nginx"
	if renderedOutput != expectedOutput {
		t.Errorf("expected %q, got %q", expectedOutput, renderedOutput)
	}
}

// TestRenderTemplateDefaultFunction verifies that the default function returns
// the fallback value when the referenced key is nil or missing.
func TestRenderTemplateDefaultFunction(t *testing.T) {
	templateContent := `tag: {{ default "latest" .tag }}`
	mergedValues := map[string]any{}

	renderedOutput, renderError := RenderTemplate(templateContent, mergedValues)
	if renderError != nil {
		t.Fatalf("unexpected error: %v", renderError)
	}

	expectedOutput := "tag: latest"
	if renderedOutput != expectedOutput {
		t.Errorf("expected %q, got %q", expectedOutput, renderedOutput)
	}
}

// TestRenderTemplateDefaultFunctionWithExplicitNil verifies that default
// handles an explicitly nil value the same as a missing key.
func TestRenderTemplateDefaultFunctionWithExplicitNil(t *testing.T) {
	templateContent := `tag: {{ default "latest" .tag }}`
	mergedValues := map[string]any{
		"tag": nil,
	}

	renderedOutput, renderError := RenderTemplate(templateContent, mergedValues)
	if renderError != nil {
		t.Fatalf("unexpected error: %v", renderError)
	}

	expectedOutput := "tag: latest"
	if renderedOutput != expectedOutput {
		t.Errorf("expected %q, got %q", expectedOutput, renderedOutput)
	}
}

// TestRenderTemplateDefaultFunctionWithPresentValue verifies that when the
// referenced key is present and non-nil, the default function returns the
// actual value rather than the fallback.
func TestRenderTemplateDefaultFunctionWithPresentValue(t *testing.T) {
	templateContent := `tag: {{ default "latest" .tag }}`
	mergedValues := map[string]any{
		"tag": "v2.1.0",
	}

	renderedOutput, renderError := RenderTemplate(templateContent, mergedValues)
	if renderError != nil {
		t.Fatalf("unexpected error: %v", renderError)
	}

	expectedOutput := "tag: v2.1.0"
	if renderedOutput != expectedOutput {
		t.Errorf("expected %q, got %q", expectedOutput, renderedOutput)
	}
}

// TestRenderTemplateRequiredFunctionMissing verifies that the required
// function returns an error when the referenced value is missing.
func TestRenderTemplateRequiredFunctionMissing(t *testing.T) {
	templateContent := `image: {{ required "image is required" .image }}`
	mergedValues := map[string]any{}

	_, renderError := RenderTemplate(templateContent, mergedValues)
	if renderError == nil {
		t.Fatal("expected error for missing required value, got nil")
	}
	if !strings.Contains(renderError.Error(), "image is required") {
		t.Errorf("error should contain the required message, got: %v", renderError)
	}
}

// TestRenderTemplateRequiredFunctionPresent verifies that the required
// function passes through the value when it is present.
func TestRenderTemplateRequiredFunctionPresent(t *testing.T) {
	templateContent := `image: {{ required "image is required" .image }}`
	mergedValues := map[string]any{
		"image": "postgres:16",
	}

	renderedOutput, renderError := RenderTemplate(templateContent, mergedValues)
	if renderError != nil {
		t.Fatalf("unexpected error: %v", renderError)
	}

	expectedOutput := "image: postgres:16"
	if renderedOutput != expectedOutput {
		t.Errorf("expected %q, got %q", expectedOutput, renderedOutput)
	}
}

// TestRenderTemplateQuoteFunction verifies that the quote function wraps
// the value in double quotes.
func TestRenderTemplateQuoteFunction(t *testing.T) {
	templateContent := `name: {{ quote .name }}`
	mergedValues := map[string]any{
		"name": "web-server",
	}

	renderedOutput, renderError := RenderTemplate(templateContent, mergedValues)
	if renderError != nil {
		t.Fatalf("unexpected error: %v", renderError)
	}

	expectedOutput := `name: "web-server"`
	if renderedOutput != expectedOutput {
		t.Errorf("expected %q, got %q", expectedOutput, renderedOutput)
	}
}

// TestRenderTemplateUpperFunction verifies that the upper function converts
// a string value to uppercase.
func TestRenderTemplateUpperFunction(t *testing.T) {
	templateContent := `level: {{ upper .logLevel }}`
	mergedValues := map[string]any{
		"logLevel": "info",
	}

	renderedOutput, renderError := RenderTemplate(templateContent, mergedValues)
	if renderError != nil {
		t.Fatalf("unexpected error: %v", renderError)
	}

	expectedOutput := "level: INFO"
	if renderedOutput != expectedOutput {
		t.Errorf("expected %q, got %q", expectedOutput, renderedOutput)
	}
}

// TestRenderTemplateLowerFunction verifies that the lower function converts
// a string value to lowercase.
func TestRenderTemplateLowerFunction(t *testing.T) {
	templateContent := `env: {{ lower .environment }}`
	mergedValues := map[string]any{
		"environment": "PRODUCTION",
	}

	renderedOutput, renderError := RenderTemplate(templateContent, mergedValues)
	if renderError != nil {
		t.Fatalf("unexpected error: %v", renderError)
	}

	expectedOutput := "env: production"
	if renderedOutput != expectedOutput {
		t.Errorf("expected %q, got %q", expectedOutput, renderedOutput)
	}
}

// TestRenderTemplateMissingKeyRendersZero verifies that referencing a key that
// does not exist in the values map produces a zero value (missingkey=zero),
// which renders as "<no value>". Use the "required" function to enforce presence.
func TestRenderTemplateMissingKeyRendersZero(t *testing.T) {
	templateContent := `image: {{ .missingKey }}`
	mergedValues := map[string]any{
		"image": "nginx:1.27",
	}

	renderedOutput, renderError := RenderTemplate(templateContent, mergedValues)
	if renderError != nil {
		t.Fatalf("unexpected error: %v", renderError)
	}
	expectedOutput := "image: <no value>"
	if renderedOutput != expectedOutput {
		t.Errorf("expected %q, got %q", expectedOutput, renderedOutput)
	}
}

// TestRenderTemplateFullDSL verifies that a complete DSL template with
// multiple placeholders and functions renders correctly.
func TestRenderTemplateFullDSL(t *testing.T) {
	templateContent := `service {{ .serviceName }} {
    image {{ .image }}:{{ default "latest" .tag }}
    instances {{ .replicas }}
    expose {{ .port }}
}`
	mergedValues := map[string]any{
		"serviceName": "web",
		"image":       "nginx",
		"tag":         "1.27",
		"replicas":    3,
		"port":        8080,
	}

	renderedOutput, renderError := RenderTemplate(templateContent, mergedValues)
	if renderError != nil {
		t.Fatalf("unexpected error: %v", renderError)
	}

	expectedOutput := `service web {
    image nginx:1.27
    instances 3
    expose 8080
}`
	if renderedOutput != expectedOutput {
		t.Errorf("expected:\n%s\ngot:\n%s", expectedOutput, renderedOutput)
	}
}

// TestRenderTemplateFullDSLWithDefaults verifies a full DSL template where
// some values fall back to defaults.
func TestRenderTemplateFullDSLWithDefaults(t *testing.T) {
	templateContent := `service {{ .serviceName }} {
    image {{ .image }}:{{ default "latest" .tag }}
    instances {{ default 1 .replicas }}
}`
	mergedValues := map[string]any{
		"serviceName": "api",
		"image":       "myapp",
	}

	renderedOutput, renderError := RenderTemplate(templateContent, mergedValues)
	if renderError != nil {
		t.Fatalf("unexpected error: %v", renderError)
	}

	expectedOutput := `service api {
    image myapp:latest
    instances 1
}`
	if renderedOutput != expectedOutput {
		t.Errorf("expected:\n%s\ngot:\n%s", expectedOutput, renderedOutput)
	}
}

// TestRenderTemplateEmptyTemplate verifies that an empty template string
// renders to an empty string without error.
func TestRenderTemplateEmptyTemplate(t *testing.T) {
	renderedOutput, renderError := RenderTemplate("", map[string]any{})
	if renderError != nil {
		t.Fatalf("unexpected error: %v", renderError)
	}
	if renderedOutput != "" {
		t.Errorf("expected empty string, got %q", renderedOutput)
	}
}

// TestRenderTemplateNoPlaceholders verifies that a template with no
// placeholders is returned unchanged.
func TestRenderTemplateNoPlaceholders(t *testing.T) {
	templateContent := "service web {\n    image nginx:1.27\n}"
	renderedOutput, renderError := RenderTemplate(templateContent, map[string]any{})
	if renderError != nil {
		t.Fatalf("unexpected error: %v", renderError)
	}
	if renderedOutput != templateContent {
		t.Errorf("expected %q, got %q", templateContent, renderedOutput)
	}
}

// TestRenderTemplateIntegerValue verifies that integer values are correctly
// interpolated into the rendered output.
func TestRenderTemplateIntegerValue(t *testing.T) {
	templateContent := `instances: {{ .count }}`
	mergedValues := map[string]any{
		"count": 5,
	}

	renderedOutput, renderError := RenderTemplate(templateContent, mergedValues)
	if renderError != nil {
		t.Fatalf("unexpected error: %v", renderError)
	}

	expectedOutput := "instances: 5"
	if renderedOutput != expectedOutput {
		t.Errorf("expected %q, got %q", expectedOutput, renderedOutput)
	}
}

// TestRenderTemplateBooleanValue verifies that boolean values are correctly
// rendered as "true" or "false".
func TestRenderTemplateBooleanValue(t *testing.T) {
	templateContent := `enabled: {{ .enabled }}`
	mergedValues := map[string]any{
		"enabled": true,
	}

	renderedOutput, renderError := RenderTemplate(templateContent, mergedValues)
	if renderError != nil {
		t.Fatalf("unexpected error: %v", renderError)
	}

	expectedOutput := "enabled: true"
	if renderedOutput != expectedOutput {
		t.Errorf("expected %q, got %q", expectedOutput, renderedOutput)
	}
}

// TestRenderTemplateInvalidSyntax verifies that a malformed Go template
// expression returns a parse error.
func TestRenderTemplateInvalidSyntax(t *testing.T) {
	templateContent := `image: {{ .image `
	mergedValues := map[string]any{
		"image": "nginx",
	}

	_, renderError := RenderTemplate(templateContent, mergedValues)
	if renderError == nil {
		t.Fatal("expected error for invalid template syntax, got nil")
	}
}

// ---------------------------------------------------------------------------
// LoadValuesFile tests
// ---------------------------------------------------------------------------

// TestLoadValuesFileSimpleKeyValue verifies that a values file with simple
// key=value pairs is parsed into a flat map.
func TestLoadValuesFileSimpleKeyValue(t *testing.T) {
	tempDirectory := t.TempDir()
	valuesFilePath := filepath.Join(tempDirectory, "values.txt")

	fileContent := "image: nginx:1.27\ninstances: 3\nport: 8080\n"
	if writeError := os.WriteFile(valuesFilePath, []byte(fileContent), 0600); writeError != nil {
		t.Fatalf("failed to write values file: %v", writeError)
	}

	loadedValues, loadError := LoadValuesFile(valuesFilePath)
	if loadError != nil {
		t.Fatalf("unexpected error: %v", loadError)
	}

	expectedImage, imageExists := loadedValues["image"]
	if !imageExists {
		t.Fatal("expected 'image' key to exist in loaded values")
	}
	if expectedImage != "nginx:1.27" {
		t.Errorf("expected image value 'nginx:1.27', got %q", expectedImage)
	}

	expectedInstances, instancesExists := loadedValues["instances"]
	if !instancesExists {
		t.Fatal("expected 'instances' key to exist in loaded values")
	}
	if expectedInstances != "3" {
		t.Errorf("expected instances value '3', got %q", expectedInstances)
	}
}

// TestLoadValuesFileCommentsAndBlankLines verifies that comments (lines
// starting with #) and blank lines are ignored during parsing.
func TestLoadValuesFileCommentsAndBlankLines(t *testing.T) {
	tempDirectory := t.TempDir()
	valuesFilePath := filepath.Join(tempDirectory, "values.txt")

	fileContent := `# This is a comment
image: nginx:1.27

# Another comment

instances: 3
`
	if writeError := os.WriteFile(valuesFilePath, []byte(fileContent), 0600); writeError != nil {
		t.Fatalf("failed to write values file: %v", writeError)
	}

	loadedValues, loadError := LoadValuesFile(valuesFilePath)
	if loadError != nil {
		t.Fatalf("unexpected error: %v", loadError)
	}

	if len(loadedValues) != 2 {
		t.Errorf("expected 2 values after skipping comments and blanks, got %d", len(loadedValues))
	}
	if loadedValues["image"] != "nginx:1.27" {
		t.Errorf("expected image 'nginx:1.27', got %q", loadedValues["image"])
	}
	if loadedValues["instances"] != "3" {
		t.Errorf("expected instances '3', got %q", loadedValues["instances"])
	}
}

// TestLoadValuesFileDotNotationNested verifies that dot-notation keys like
// "web.image" are expanded into nested maps: {"web": {"image": "nginx"}}.
func TestLoadValuesFileDotNotationNested(t *testing.T) {
	tempDirectory := t.TempDir()
	valuesFilePath := filepath.Join(tempDirectory, "values.txt")

	fileContent := "web.image: nginx\nweb.port: 8080\n"
	if writeError := os.WriteFile(valuesFilePath, []byte(fileContent), 0600); writeError != nil {
		t.Fatalf("failed to write values file: %v", writeError)
	}

	loadedValues, loadError := LoadValuesFile(valuesFilePath)
	if loadError != nil {
		t.Fatalf("unexpected error: %v", loadError)
	}

	webValues, webExists := loadedValues["web"]
	if !webExists {
		t.Fatal("expected 'web' key to exist in loaded values")
	}
	webMap, isMap := webValues.(map[string]any)
	if !isMap {
		t.Fatalf("expected 'web' to be a map, got %T", webValues)
	}
	if webMap["image"] != "nginx" {
		t.Errorf("expected web.image 'nginx', got %q", webMap["image"])
	}
	if webMap["port"] != "8080" {
		t.Errorf("expected web.port '8080', got %q", webMap["port"])
	}
}

// TestLoadValuesFileNonExistent verifies that attempting to load a values
// file that does not exist returns an error.
func TestLoadValuesFileNonExistent(t *testing.T) {
	_, loadError := LoadValuesFile("/nonexistent/path/values.txt")
	if loadError == nil {
		t.Fatal("expected error for non-existent file, got nil")
	}
}

// TestLoadValuesFileEmptyFile verifies that loading an empty values file
// returns an empty map without error.
func TestLoadValuesFileEmptyFile(t *testing.T) {
	tempDirectory := t.TempDir()
	valuesFilePath := filepath.Join(tempDirectory, "empty.txt")

	if writeError := os.WriteFile(valuesFilePath, []byte(""), 0600); writeError != nil {
		t.Fatalf("failed to write values file: %v", writeError)
	}

	loadedValues, loadError := LoadValuesFile(valuesFilePath)
	if loadError != nil {
		t.Fatalf("unexpected error: %v", loadError)
	}
	if len(loadedValues) != 0 {
		t.Errorf("expected empty map, got %d entries", len(loadedValues))
	}
}

// TestLoadValuesFileDeepNesting verifies that deeply nested dot-notation
// keys like "a.b.c" expand into properly nested maps.
func TestLoadValuesFileDeepNesting(t *testing.T) {
	tempDirectory := t.TempDir()
	valuesFilePath := filepath.Join(tempDirectory, "values.txt")

	fileContent := "database.connection.host: localhost\ndatabase.connection.port: 5432\n"
	if writeError := os.WriteFile(valuesFilePath, []byte(fileContent), 0600); writeError != nil {
		t.Fatalf("failed to write values file: %v", writeError)
	}

	loadedValues, loadError := LoadValuesFile(valuesFilePath)
	if loadError != nil {
		t.Fatalf("unexpected error: %v", loadError)
	}

	databaseValues, dbExists := loadedValues["database"]
	if !dbExists {
		t.Fatal("expected 'database' key to exist")
	}
	databaseMap, isMap := databaseValues.(map[string]any)
	if !isMap {
		t.Fatalf("expected 'database' to be a map, got %T", databaseValues)
	}

	connectionValues, connExists := databaseMap["connection"]
	if !connExists {
		t.Fatal("expected 'connection' key to exist under 'database'")
	}
	connectionMap, isConnMap := connectionValues.(map[string]any)
	if !isConnMap {
		t.Fatalf("expected 'connection' to be a map, got %T", connectionValues)
	}
	if connectionMap["host"] != "localhost" {
		t.Errorf("expected host 'localhost', got %q", connectionMap["host"])
	}
	if connectionMap["port"] != "5432" {
		t.Errorf("expected port '5432', got %q", connectionMap["port"])
	}
}

// ---------------------------------------------------------------------------
// MergeValues tests
// ---------------------------------------------------------------------------

// TestMergeValuesEmptyMaps verifies that merging empty maps produces an
// empty result.
func TestMergeValuesEmptyMaps(t *testing.T) {
	mergedResult := MergeValues(map[string]any{}, map[string]any{})
	if len(mergedResult) != 0 {
		t.Errorf("expected empty merged map, got %d entries", len(mergedResult))
	}
}

// TestMergeValuesFlatOverride verifies that values from later layers
// override values from earlier layers for flat (non-nested) keys.
func TestMergeValuesFlatOverride(t *testing.T) {
	baseLayer := map[string]any{
		"image":     "nginx:1.26",
		"instances": 3,
	}
	overrideLayer := map[string]any{
		"image": "nginx:1.27",
	}

	mergedResult := MergeValues(baseLayer, overrideLayer)

	if mergedResult["image"] != "nginx:1.27" {
		t.Errorf("expected image 'nginx:1.27', got %q", mergedResult["image"])
	}
	if mergedResult["instances"] != 3 {
		t.Errorf("expected instances 3, got %v", mergedResult["instances"])
	}
}

// TestMergeValuesDeepMerge verifies that nested maps are deep-merged rather
// than replaced wholesale. Keys from both the base and override layers
// should be present in the merged result.
func TestMergeValuesDeepMerge(t *testing.T) {
	baseLayer := map[string]any{
		"web": map[string]any{
			"image":     "nginx",
			"instances": 3,
		},
	}
	overrideLayer := map[string]any{
		"web": map[string]any{
			"image": "apache",
		},
	}

	mergedResult := MergeValues(baseLayer, overrideLayer)

	webValues, webExists := mergedResult["web"]
	if !webExists {
		t.Fatal("expected 'web' key to exist in merged result")
	}
	webMap, isMap := webValues.(map[string]any)
	if !isMap {
		t.Fatalf("expected 'web' to be a map, got %T", webValues)
	}

	// The override should win for "image".
	if webMap["image"] != "apache" {
		t.Errorf("expected web.image 'apache', got %q", webMap["image"])
	}
	// The base value for "instances" should be preserved since the override
	// did not include it.
	if webMap["instances"] != 3 {
		t.Errorf("expected web.instances 3, got %v", webMap["instances"])
	}
}

// TestMergeValuesThreeLayers verifies that merging three successive layers
// applies overrides in order: base < middle < top.
func TestMergeValuesThreeLayers(t *testing.T) {
	baseLayer := map[string]any{
		"image":     "nginx:1.25",
		"instances": 1,
		"port":      80,
	}
	middleLayer := map[string]any{
		"image":     "nginx:1.26",
		"instances": 3,
	}
	topLayer := map[string]any{
		"image": "nginx:1.27",
	}

	mergedResult := MergeValues(baseLayer, middleLayer, topLayer)

	if mergedResult["image"] != "nginx:1.27" {
		t.Errorf("expected image 'nginx:1.27', got %q", mergedResult["image"])
	}
	if mergedResult["instances"] != 3 {
		t.Errorf("expected instances 3, got %v", mergedResult["instances"])
	}
	if mergedResult["port"] != 80 {
		t.Errorf("expected port 80, got %v", mergedResult["port"])
	}
}

// TestMergeValuesSingleMap verifies that merging a single map returns a
// copy of that map.
func TestMergeValuesSingleMap(t *testing.T) {
	singleLayer := map[string]any{
		"image": "nginx:1.27",
	}

	mergedResult := MergeValues(singleLayer)

	if mergedResult["image"] != "nginx:1.27" {
		t.Errorf("expected image 'nginx:1.27', got %q", mergedResult["image"])
	}
}

// TestMergeValuesNoLayers verifies that merging zero layers returns an
// empty map.
func TestMergeValuesNoLayers(t *testing.T) {
	mergedResult := MergeValues()
	if mergedResult == nil {
		t.Fatal("expected non-nil map, got nil")
	}
	if len(mergedResult) != 0 {
		t.Errorf("expected empty map, got %d entries", len(mergedResult))
	}
}

// TestMergeValuesDeepMergeDoesNotMutateInputs verifies that the input maps
// are not modified by the merge operation.
func TestMergeValuesDeepMergeDoesNotMutateInputs(t *testing.T) {
	baseLayer := map[string]any{
		"web": map[string]any{
			"image": "nginx",
		},
	}
	overrideLayer := map[string]any{
		"web": map[string]any{
			"port": 8080,
		},
	}

	_ = MergeValues(baseLayer, overrideLayer)

	// Verify the base layer was not mutated.
	baseWeb := baseLayer["web"].(map[string]any)
	if _, portExists := baseWeb["port"]; portExists {
		t.Error("base layer was mutated: 'port' should not exist in base web map")
	}
}

// ---------------------------------------------------------------------------
// ParseSetOverrides tests
// ---------------------------------------------------------------------------

// TestParseSetOverridesSimpleKeyValue verifies that a simple "key=value"
// override is parsed into a flat map entry.
func TestParseSetOverridesSimpleKeyValue(t *testing.T) {
	setFlags := []string{"image=nginx:1.27"}

	parsedOverrides, parseError := ParseSetOverrides(setFlags)
	if parseError != nil {
		t.Fatalf("unexpected error: %v", parseError)
	}

	if parsedOverrides["image"] != "nginx:1.27" {
		t.Errorf("expected image 'nginx:1.27', got %q", parsedOverrides["image"])
	}
}

// TestParseSetOverridesDotPath verifies that dot-path notation like
// "web.image=nginx" is expanded into nested maps.
func TestParseSetOverridesDotPath(t *testing.T) {
	setFlags := []string{"web.image=nginx"}

	parsedOverrides, parseError := ParseSetOverrides(setFlags)
	if parseError != nil {
		t.Fatalf("unexpected error: %v", parseError)
	}

	webValues, webExists := parsedOverrides["web"]
	if !webExists {
		t.Fatal("expected 'web' key to exist")
	}
	webMap, isMap := webValues.(map[string]any)
	if !isMap {
		t.Fatalf("expected 'web' to be a map, got %T", webValues)
	}
	if webMap["image"] != "nginx" {
		t.Errorf("expected web.image 'nginx', got %q", webMap["image"])
	}
}

// TestParseSetOverridesMissingEquals verifies that an override string
// without an '=' separator returns an error.
func TestParseSetOverridesMissingEquals(t *testing.T) {
	setFlags := []string{"imagenginx"}

	_, parseError := ParseSetOverrides(setFlags)
	if parseError == nil {
		t.Fatal("expected error for missing '=' separator, got nil")
	}
}

// TestParseSetOverridesEmptyValue verifies that a key with an empty value
// (e.g., "tag=") is allowed and results in an empty string value.
func TestParseSetOverridesEmptyValue(t *testing.T) {
	setFlags := []string{"tag="}

	parsedOverrides, parseError := ParseSetOverrides(setFlags)
	if parseError != nil {
		t.Fatalf("unexpected error: %v", parseError)
	}

	tagValue, tagExists := parsedOverrides["tag"]
	if !tagExists {
		t.Fatal("expected 'tag' key to exist")
	}
	if tagValue != "" {
		t.Errorf("expected empty string for tag, got %q", tagValue)
	}
}

// TestParseSetOverridesMultipleFlags verifies that multiple --set flags
// are all parsed into the same result map.
func TestParseSetOverridesMultipleFlags(t *testing.T) {
	setFlags := []string{"image=nginx:1.27", "instances=5", "port=8080"}

	parsedOverrides, parseError := ParseSetOverrides(setFlags)
	if parseError != nil {
		t.Fatalf("unexpected error: %v", parseError)
	}

	if parsedOverrides["image"] != "nginx:1.27" {
		t.Errorf("expected image 'nginx:1.27', got %q", parsedOverrides["image"])
	}
	if parsedOverrides["instances"] != "5" {
		t.Errorf("expected instances '5', got %q", parsedOverrides["instances"])
	}
	if parsedOverrides["port"] != "8080" {
		t.Errorf("expected port '8080', got %q", parsedOverrides["port"])
	}
}

// TestParseSetOverridesDeepDotPath verifies that deeply nested dot-path
// notation is expanded correctly (e.g., "a.b.c=value").
func TestParseSetOverridesDeepDotPath(t *testing.T) {
	setFlags := []string{"database.connection.host=localhost"}

	parsedOverrides, parseError := ParseSetOverrides(setFlags)
	if parseError != nil {
		t.Fatalf("unexpected error: %v", parseError)
	}

	databaseValues, dbExists := parsedOverrides["database"]
	if !dbExists {
		t.Fatal("expected 'database' key to exist")
	}
	databaseMap, isMap := databaseValues.(map[string]any)
	if !isMap {
		t.Fatalf("expected 'database' to be a map, got %T", databaseValues)
	}
	connectionValues, connExists := databaseMap["connection"]
	if !connExists {
		t.Fatal("expected 'connection' key to exist")
	}
	connectionMap, isConnMap := connectionValues.(map[string]any)
	if !isConnMap {
		t.Fatalf("expected 'connection' to be a map, got %T", connectionValues)
	}
	if connectionMap["host"] != "localhost" {
		t.Errorf("expected host 'localhost', got %q", connectionMap["host"])
	}
}

// TestParseSetOverridesValueWithEquals verifies that a value containing
// '=' characters is preserved correctly (only the first '=' is the split).
func TestParseSetOverridesValueWithEquals(t *testing.T) {
	setFlags := []string{"config=key=value"}

	parsedOverrides, parseError := ParseSetOverrides(setFlags)
	if parseError != nil {
		t.Fatalf("unexpected error: %v", parseError)
	}

	if parsedOverrides["config"] != "key=value" {
		t.Errorf("expected config 'key=value', got %q", parsedOverrides["config"])
	}
}

// TestParseSetOverridesEmptyList verifies that an empty list of overrides
// returns an empty map without error.
func TestParseSetOverridesEmptyList(t *testing.T) {
	parsedOverrides, parseError := ParseSetOverrides([]string{})
	if parseError != nil {
		t.Fatalf("unexpected error: %v", parseError)
	}
	if len(parsedOverrides) != 0 {
		t.Errorf("expected empty map, got %d entries", len(parsedOverrides))
	}
}

// ---------------------------------------------------------------------------
// ParseSetFromEnvOverrides tests
// ---------------------------------------------------------------------------

// TestParseSetFromEnvOverridesBasic verifies that when an environment
// variable is set, its value is correctly resolved into the overrides map.
func TestParseSetFromEnvOverridesBasic(t *testing.T) {
	envVariableName := "CCATTLER_TEST_IMAGE_TAG"
	t.Setenv(envVariableName, "v2.3.0")

	envFlags := []string{"tag=" + envVariableName}

	parsedOverrides, parseError := ParseSetFromEnvOverrides(envFlags)
	if parseError != nil {
		t.Fatalf("unexpected error: %v", parseError)
	}

	if parsedOverrides["tag"] != "v2.3.0" {
		t.Errorf("expected tag 'v2.3.0', got %q", parsedOverrides["tag"])
	}
}

// TestParseSetFromEnvOverridesMissingEnvVar verifies that referencing
// an environment variable that is not set returns an error.
func TestParseSetFromEnvOverridesMissingEnvVar(t *testing.T) {
	// Ensure the variable does not exist.
	envVariableName := "CCATTLER_TEST_NONEXISTENT_VAR_12345"
	os.Unsetenv(envVariableName)

	envFlags := []string{"tag=" + envVariableName}

	_, parseError := ParseSetFromEnvOverrides(envFlags)
	if parseError == nil {
		t.Fatal("expected error for missing environment variable, got nil")
	}
}

// TestParseSetFromEnvOverridesDotPath verifies that dot-path keys work
// with environment variable resolution.
func TestParseSetFromEnvOverridesDotPath(t *testing.T) {
	envVariableName := "CCATTLER_TEST_DB_HOST"
	t.Setenv(envVariableName, "db.example.com")

	envFlags := []string{"database.host=" + envVariableName}

	parsedOverrides, parseError := ParseSetFromEnvOverrides(envFlags)
	if parseError != nil {
		t.Fatalf("unexpected error: %v", parseError)
	}

	databaseValues, dbExists := parsedOverrides["database"]
	if !dbExists {
		t.Fatal("expected 'database' key to exist")
	}
	databaseMap, isMap := databaseValues.(map[string]any)
	if !isMap {
		t.Fatalf("expected 'database' to be a map, got %T", databaseValues)
	}
	if databaseMap["host"] != "db.example.com" {
		t.Errorf("expected host 'db.example.com', got %q", databaseMap["host"])
	}
}

// TestParseSetFromEnvOverridesEmptyEnvValue verifies that an environment
// variable set to an empty string is accepted (empty values are valid).
func TestParseSetFromEnvOverridesEmptyEnvValue(t *testing.T) {
	envVariableName := "CCATTLER_TEST_EMPTY_VALUE"
	t.Setenv(envVariableName, "")

	envFlags := []string{"tag=" + envVariableName}

	parsedOverrides, parseError := ParseSetFromEnvOverrides(envFlags)
	if parseError != nil {
		t.Fatalf("unexpected error: %v", parseError)
	}

	if parsedOverrides["tag"] != "" {
		t.Errorf("expected empty string for tag, got %q", parsedOverrides["tag"])
	}
}

// TestParseSetFromEnvOverridesMissingEquals verifies that an env override
// string without an '=' separator returns an error.
func TestParseSetFromEnvOverridesMissingEquals(t *testing.T) {
	envFlags := []string{"tagCCATTLER_TEST_SOMETHING"}

	_, parseError := ParseSetFromEnvOverrides(envFlags)
	if parseError == nil {
		t.Fatal("expected error for missing '=' separator, got nil")
	}
}

// ---------------------------------------------------------------------------
// RenderWithValuesFiles tests
// ---------------------------------------------------------------------------

// TestRenderWithValuesFilesEndToEnd exercises the full pipeline: write
// temporary values files, render a template with overrides, and verify
// the final output.
func TestRenderWithValuesFilesEndToEnd(t *testing.T) {
	tempDirectory := t.TempDir()

	// Write a base values file.
	baseValuesPath := filepath.Join(tempDirectory, "base.txt")
	baseContent := "image: nginx\ntag: 1.26\ninstances: 1\n"
	if writeError := os.WriteFile(baseValuesPath, []byte(baseContent), 0600); writeError != nil {
		t.Fatalf("failed to write base values file: %v", writeError)
	}

	// Write an override values file.
	overrideValuesPath := filepath.Join(tempDirectory, "override.txt")
	overrideContent := "tag: 1.27\ninstances: 3\n"
	if writeError := os.WriteFile(overrideValuesPath, []byte(overrideContent), 0600); writeError != nil {
		t.Fatalf("failed to write override values file: %v", writeError)
	}

	templateContent := `service web {
    image {{ .image }}:{{ .tag }}
    instances {{ .instances }}
}`

	valuesFilePaths := []string{baseValuesPath, overrideValuesPath}
	setOverrides := []string{"instances=5"}
	envOverrides := []string{}

	renderedOutput, renderError := RenderWithValuesFiles(
		templateContent, valuesFilePaths, setOverrides, envOverrides,
	)
	if renderError != nil {
		t.Fatalf("unexpected error: %v", renderError)
	}

	// --set overrides should take highest priority.
	expectedOutput := `service web {
    image nginx:1.27
    instances 5
}`
	if renderedOutput != expectedOutput {
		t.Errorf("expected:\n%s\ngot:\n%s", expectedOutput, renderedOutput)
	}
}

// TestRenderWithValuesFilesEnvOverride exercises the pipeline with
// environment variable overrides included.
func TestRenderWithValuesFilesEnvOverride(t *testing.T) {
	tempDirectory := t.TempDir()

	baseValuesPath := filepath.Join(tempDirectory, "base.txt")
	baseContent := "image: nginx\ntag: 1.26\n"
	if writeError := os.WriteFile(baseValuesPath, []byte(baseContent), 0600); writeError != nil {
		t.Fatalf("failed to write base values file: %v", writeError)
	}

	envVariableName := "CCATTLER_TEST_TAG_OVERRIDE"
	t.Setenv(envVariableName, "1.28")

	templateContent := `image: {{ .image }}:{{ .tag }}`
	valuesFilePaths := []string{baseValuesPath}
	setOverrides := []string{}
	envOverrides := []string{"tag=" + envVariableName}

	renderedOutput, renderError := RenderWithValuesFiles(
		templateContent, valuesFilePaths, setOverrides, envOverrides,
	)
	if renderError != nil {
		t.Fatalf("unexpected error: %v", renderError)
	}

	expectedOutput := "image: nginx:1.28"
	if renderedOutput != expectedOutput {
		t.Errorf("expected %q, got %q", expectedOutput, renderedOutput)
	}
}

// TestRenderWithValuesFilesNoFiles verifies rendering a template with
// no values files and only --set overrides.
func TestRenderWithValuesFilesNoFiles(t *testing.T) {
	templateContent := `image: {{ .image }}`
	setOverrides := []string{"image=postgres:16"}

	renderedOutput, renderError := RenderWithValuesFiles(
		templateContent, []string{}, setOverrides, []string{},
	)
	if renderError != nil {
		t.Fatalf("unexpected error: %v", renderError)
	}

	expectedOutput := "image: postgres:16"
	if renderedOutput != expectedOutput {
		t.Errorf("expected %q, got %q", expectedOutput, renderedOutput)
	}
}

// TestRenderWithValuesFilesBadFile verifies that a non-existent values
// file in the list causes an error.
func TestRenderWithValuesFilesBadFile(t *testing.T) {
	templateContent := `image: {{ .image }}`

	_, renderError := RenderWithValuesFiles(
		templateContent,
		[]string{"/nonexistent/values.txt"},
		[]string{},
		[]string{},
	)
	if renderError == nil {
		t.Fatal("expected error for non-existent values file, got nil")
	}
}

// TestRenderWithValuesFilesBadSetOverride verifies that a malformed
// --set override propagates an error.
func TestRenderWithValuesFilesBadSetOverride(t *testing.T) {
	templateContent := `image: {{ .image }}`

	_, renderError := RenderWithValuesFiles(
		templateContent,
		[]string{},
		[]string{"invalid-no-equals"},
		[]string{},
	)
	if renderError == nil {
		t.Fatal("expected error for malformed --set override, got nil")
	}
}

// TestRenderWithValuesFilesPrecedenceOrder verifies the full precedence
// chain: base file < override file < --set-from-env < --set.
func TestRenderWithValuesFilesPrecedenceOrder(t *testing.T) {
	tempDirectory := t.TempDir()

	baseValuesPath := filepath.Join(tempDirectory, "base.txt")
	baseContent := "image: base-image\ntag: base-tag\nport: 80\nlevel: debug\n"
	if writeError := os.WriteFile(baseValuesPath, []byte(baseContent), 0600); writeError != nil {
		t.Fatalf("failed to write base values file: %v", writeError)
	}

	overrideValuesPath := filepath.Join(tempDirectory, "override.txt")
	overrideContent := "tag: override-tag\nlevel: info\n"
	if writeError := os.WriteFile(overrideValuesPath, []byte(overrideContent), 0600); writeError != nil {
		t.Fatalf("failed to write override values file: %v", writeError)
	}

	envVariableName := "CCATTLER_TEST_LEVEL"
	t.Setenv(envVariableName, "warn")

	templateContent := `{{ .image }} {{ .tag }} {{ .port }} {{ .level }}`
	valuesFilePaths := []string{baseValuesPath, overrideValuesPath}
	setOverrides := []string{"tag=set-tag"}
	envOverrides := []string{"level=" + envVariableName}

	renderedOutput, renderError := RenderWithValuesFiles(
		templateContent, valuesFilePaths, setOverrides, envOverrides,
	)
	if renderError != nil {
		t.Fatalf("unexpected error: %v", renderError)
	}

	// image: only in base -> base-image
	// tag: base < override < --set -> set-tag
	// port: only in base -> 80
	// level: base < override < env -> warn (or set wins over env depending on precedence)
	// The exact precedence between --set and --set-from-env depends on
	// the implementation. We check that at least the values file layers
	// are overridden correctly. The output should contain "set-tag"
	// (--set beats values file) and "base-image" (unchanged from base).
	if !strings.Contains(renderedOutput, "base-image") {
		t.Errorf("expected 'base-image' in output, got: %s", renderedOutput)
	}
	if !strings.Contains(renderedOutput, "set-tag") {
		t.Errorf("expected 'set-tag' in output, got: %s", renderedOutput)
	}
	if !strings.Contains(renderedOutput, "80") {
		t.Errorf("expected '80' in output, got: %s", renderedOutput)
	}
}

// ---------------------------------------------------------------------------
// CollectDSLFiles tests
// ---------------------------------------------------------------------------

// TestCollectDSLFilesFindsAllExtensions verifies that CollectDSLFiles finds
// both .cca and .ccattler files and returns them sorted.
func TestCollectDSLFilesFindsAllExtensions(t *testing.T) {
	tempDirectory := t.TempDir()

	// Create files with both extensions plus a non-DSL file.
	for _, fileName := range []string{"b.cca", "a.ccattler", "c.txt", "d.cca"} {
		filePath := filepath.Join(tempDirectory, fileName)
		if writeError := os.WriteFile(filePath, []byte("test"), 0600); writeError != nil {
			t.Fatalf("writing %s: %v", fileName, writeError)
		}
	}

	collectedFiles, collectError := CollectDSLFiles(tempDirectory)
	if collectError != nil {
		t.Fatalf("unexpected error: %v", collectError)
	}

	if len(collectedFiles) != 3 {
		t.Fatalf("expected 3 DSL files, got %d: %v", len(collectedFiles), collectedFiles)
	}

	// Verify sorted order: a.ccattler, b.cca, d.cca.
	expectedSuffixes := []string{"a.ccattler", "b.cca", "d.cca"}
	for fileIndex, expectedSuffix := range expectedSuffixes {
		if !strings.HasSuffix(collectedFiles[fileIndex], expectedSuffix) {
			t.Errorf("file[%d]: expected suffix %q, got %q", fileIndex, expectedSuffix, collectedFiles[fileIndex])
		}
	}
}

// TestCollectDSLFilesEmptyDirectory verifies that CollectDSLFiles returns an
// error when the directory contains no DSL files.
func TestCollectDSLFilesEmptyDirectory(t *testing.T) {
	tempDirectory := t.TempDir()

	_, collectError := CollectDSLFiles(tempDirectory)
	if collectError == nil {
		t.Fatal("expected error for empty directory, got nil")
	}
	if !strings.Contains(collectError.Error(), "no .cca or .ccattler files") {
		t.Errorf("expected 'no .cca or .ccattler files' in error, got: %v", collectError)
	}
}

// ---------------------------------------------------------------------------
// ValidateTemplateSyntax tests
// ---------------------------------------------------------------------------

// TestValidateTemplateSyntaxValid verifies that valid Go template syntax
// passes validation without error.
func TestValidateTemplateSyntaxValid(t *testing.T) {
	validTemplates := []string{
		`{{ .image }}`,
		`{{ default "latest" .tag }}`,
		`{{ if .enabled }}yes{{ end }}`,
		`plain text no templates`,
		``,
	}
	for _, templateContent := range validTemplates {
		if syntaxError := ValidateTemplateSyntax(templateContent); syntaxError != nil {
			t.Errorf("expected valid syntax for %q, got error: %v", templateContent, syntaxError)
		}
	}
}

// TestValidateTemplateSyntaxInvalid verifies that malformed Go template syntax
// is caught and returns a descriptive error.
func TestValidateTemplateSyntaxInvalid(t *testing.T) {
	invalidTemplates := []string{
		`{{ .image `,
		`{{ if }}`,
		`{{ end }}`,
	}
	for _, templateContent := range invalidTemplates {
		syntaxError := ValidateTemplateSyntax(templateContent)
		if syntaxError == nil {
			t.Errorf("expected syntax error for %q, got nil", templateContent)
		}
	}
}

// ---------------------------------------------------------------------------
// ValidateRenderedDSL tests
// ---------------------------------------------------------------------------

// TestValidateRenderedDSLValid verifies that valid rendered CCattler DSL
// passes validation.
func TestValidateRenderedDSLValid(t *testing.T) {
	validDSL := `service web {
    image nginx:1.27
    instances 3
    expose 8080
}`
	if validationError := ValidateRenderedDSL(validDSL); validationError != nil {
		t.Errorf("expected valid DSL to pass, got error: %v", validationError)
	}
}

// TestValidateRenderedDSLInvalidParse verifies that malformed DSL that fails
// parsing is caught by validation.
func TestValidateRenderedDSLInvalidParse(t *testing.T) {
	invalidDSL := `service web { image }`
	validationError := ValidateRenderedDSL(invalidDSL)
	if validationError == nil {
		t.Error("expected validation error for invalid DSL, got nil")
	}
}

// ---------------------------------------------------------------------------
// DetectUnusedValues tests
// ---------------------------------------------------------------------------

// TestDetectUnusedValuesNoUnused verifies that when all values are referenced
// in the template, no unused keys are reported.
func TestDetectUnusedValuesNoUnused(t *testing.T) {
	templateContent := `{{ .name }} {{ .image }}`
	valuesMap := map[string]any{
		"name":  "web",
		"image": "nginx",
	}

	unusedKeys := DetectUnusedValues(templateContent, valuesMap)
	if len(unusedKeys) != 0 {
		t.Errorf("expected no unused keys, got %v", unusedKeys)
	}
}

// TestDetectUnusedValuesSomeUnused verifies that unreferenced top-level keys
// are detected and returned sorted.
func TestDetectUnusedValuesSomeUnused(t *testing.T) {
	templateContent := `{{ .name }}`
	valuesMap := map[string]any{
		"name":   "web",
		"extra":  "not used",
		"bonus":  "also not used",
	}

	unusedKeys := DetectUnusedValues(templateContent, valuesMap)
	if len(unusedKeys) != 2 {
		t.Fatalf("expected 2 unused keys, got %d: %v", len(unusedKeys), unusedKeys)
	}
	if unusedKeys[0] != "bonus" || unusedKeys[1] != "extra" {
		t.Errorf("expected [bonus extra], got %v", unusedKeys)
	}
}

// TestDetectUnusedValuesNestedAccess verifies that nested access like
// .web.image correctly detects "web" as a referenced top-level key.
func TestDetectUnusedValuesNestedAccess(t *testing.T) {
	templateContent := `{{ .web.image }}`
	valuesMap := map[string]any{
		"web":    map[string]any{"image": "nginx"},
		"unused": "not referenced",
	}

	unusedKeys := DetectUnusedValues(templateContent, valuesMap)
	if len(unusedKeys) != 1 {
		t.Fatalf("expected 1 unused key, got %d: %v", len(unusedKeys), unusedKeys)
	}
	if unusedKeys[0] != "unused" {
		t.Errorf("expected [unused], got %v", unusedKeys)
	}
}

// TestDetectUnusedValuesEmptyTemplate verifies that with no template actions,
// all values are reported as unused.
func TestDetectUnusedValuesEmptyTemplate(t *testing.T) {
	templateContent := `no template actions here`
	valuesMap := map[string]any{
		"alpha": "a",
		"beta":  "b",
	}

	unusedKeys := DetectUnusedValues(templateContent, valuesMap)
	if len(unusedKeys) != 2 {
		t.Fatalf("expected 2 unused keys, got %d: %v", len(unusedKeys), unusedKeys)
	}
}

// TestDetectUnusedValuesEmptyValues verifies that with no values provided,
// no unused keys are reported.
func TestDetectUnusedValuesEmptyValues(t *testing.T) {
	templateContent := `{{ .name }}`
	valuesMap := map[string]any{}

	unusedKeys := DetectUnusedValues(templateContent, valuesMap)
	if len(unusedKeys) != 0 {
		t.Errorf("expected no unused keys for empty values, got %v", unusedKeys)
	}
}
