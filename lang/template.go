package lang

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
)

// templateFunctions returns the custom function map available to all CCattler
// DSL templates. These are a small subset of Sprig-like utilities implemented
// without importing the full Sprig library to avoid adding a dependency.
func templateFunctions() template.FuncMap {
	return template.FuncMap{
		"default":  templateFuncDefault,
		"required": templateFuncRequired,
		"quote":    templateFuncQuote,
		"upper":    strings.ToUpper,
		"lower":    strings.ToLower,
		"title":    strings.ToTitle,
		"toJson":   templateFuncToJSON,
		"indent":   templateFuncIndent,
		"nindent":  templateFuncNindent,
		"ternary":  templateFuncTernary,
	}
}

// templateFuncDefault returns the given value if it is non-empty and non-nil,
// otherwise returns the provided default value. This mirrors Sprig's default
// function: {{ default "nginx:latest" .image }}.
func templateFuncDefault(defaultValue any, givenValue any) any {
	if givenValue == nil {
		return defaultValue
	}
	givenString, isString := givenValue.(string)
	if isString && givenString == "" {
		return defaultValue
	}
	return givenValue
}

// templateFuncRequired returns the given value if it is non-empty and non-nil,
// otherwise returns an error with the provided message. This ensures that
// critical values are always supplied: {{ required "image is required" .image }}.
func templateFuncRequired(errorMessage string, givenValue any) (any, error) {
	if givenValue == nil {
		return nil, fmt.Errorf("required value missing: %s", errorMessage)
	}
	givenString, isString := givenValue.(string)
	if isString && givenString == "" {
		return nil, fmt.Errorf("required value missing: %s", errorMessage)
	}
	return givenValue, nil
}

// templateFuncQuote wraps the given value's string representation in double
// quotes: {{ quote .value }} produces "somevalue".
func templateFuncQuote(givenValue any) string {
	return fmt.Sprintf("%q", fmt.Sprint(givenValue))
}

// templateFuncToJSON marshals the given value to a JSON string representation.
// Returns an error if the value cannot be serialized to JSON.
func templateFuncToJSON(givenValue any) (string, error) {
	jsonBytes, marshalError := json.Marshal(givenValue)
	if marshalError != nil {
		return "", fmt.Errorf("toJson: failed to marshal value: %w", marshalError)
	}
	return string(jsonBytes), nil
}

// templateFuncIndent prepends each line in the given text with the specified
// number of spaces. The first line is also indented. For example,
// {{ indent 4 .block }} indents every line by 4 spaces.
func templateFuncIndent(spaceCount int, textContent string) string {
	indentPrefix := strings.Repeat(" ", spaceCount)
	lines := strings.Split(textContent, "\n")
	for lineIndex, lineContent := range lines {
		lines[lineIndex] = indentPrefix + lineContent
	}
	return strings.Join(lines, "\n")
}

// templateFuncNindent works like indent but prepends a newline before the
// indented text. This is useful for YAML-like contexts where you want the
// content to start on the next line: {{ nindent 4 .block }}.
func templateFuncNindent(spaceCount int, textContent string) string {
	return "\n" + templateFuncIndent(spaceCount, textContent)
}

// templateFuncTernary returns trueValue if the condition is truthy, otherwise
// returns falseValue. This mirrors the ternary operator pattern:
// {{ ternary "yes" "no" .condition }}.
func templateFuncTernary(trueValue any, falseValue any, condition any) any {
	if isTruthy(condition) {
		return trueValue
	}
	return falseValue
}

// isTruthy determines whether a value is considered "truthy" for the ternary
// function. Nil is false, booleans use their natural value, empty strings are
// false, zero numeric values are false, and everything else is true.
func isTruthy(value any) bool {
	if value == nil {
		return false
	}
	switch typedValue := value.(type) {
	case bool:
		return typedValue
	case string:
		return typedValue != ""
	case int:
		return typedValue != 0
	case int64:
		return typedValue != 0
	case float64:
		return typedValue != 0
	default:
		return true
	}
}

// RenderTemplate renders a Go text/template string using the provided merged
// values map. The template has access to CCattler's custom template functions
// (default, required, quote, upper, lower, title, toJson, indent, nindent,
// ternary). The template uses Option("missingkey=error") so references to
// undefined keys produce clear errors instead of silent empty strings.
func RenderTemplate(templateContent string, mergedValues map[string]any) (string, error) {
	parsedTemplate, parseError := template.New("ccattler").
		Option("missingkey=zero").
		Funcs(templateFunctions()).
		Parse(templateContent)
	if parseError != nil {
		return "", fmt.Errorf("template parse error: %w", parseError)
	}

	var renderedOutput strings.Builder
	executeError := parsedTemplate.Execute(&renderedOutput, mergedValues)
	if executeError != nil {
		return "", fmt.Errorf("template render error: %w", executeError)
	}
	return renderedOutput.String(), nil
}

// LoadValuesFile reads a values file from disk and parses it into a nested
// map[string]any suitable for template rendering. The file format is a simple
// key-value format with one entry per line:
//
//	image: nginx:1.27
//	instances: 3
//	cpu: 500m
//	nested.key: value
//
// Lines beginning with # are comments and blank lines are skipped. Dot
// notation in keys creates nested maps: "web.image: nginx" becomes
// {"web": {"image": "nginx"}}. All values are kept as strings since the
// CCattler DSL uses string values throughout.
func LoadValuesFile(filePath string) (map[string]any, error) {
	fileHandle, openError := os.Open(filePath) //nolint:gosec // values file path comes from CLI --values flag
	if openError != nil {
		return nil, fmt.Errorf("failed to open values file %s: %w", filePath, openError)
	}
	defer func() { _ = fileHandle.Close() }()

	valuesMap := make(map[string]any)
	lineScanner := bufio.NewScanner(fileHandle)
	lineNumber := 0

	for lineScanner.Scan() {
		lineNumber++
		rawLine := lineScanner.Text()
		trimmedLine := strings.TrimSpace(rawLine)

		// Skip blank lines and comment lines.
		if trimmedLine == "" || strings.HasPrefix(trimmedLine, "#") {
			continue
		}

		// Split on the first colon to separate key from value.
		colonIndex := strings.IndexByte(trimmedLine, ':')
		if colonIndex < 0 {
			return nil, fmt.Errorf("%s:%d: invalid line (expected key: value format): %s", filePath, lineNumber, trimmedLine)
		}

		keyPart := strings.TrimSpace(trimmedLine[:colonIndex])
		valuePart := strings.TrimSpace(trimmedLine[colonIndex+1:])

		if keyPart == "" {
			return nil, fmt.Errorf("%s:%d: empty key in line: %s", filePath, lineNumber, trimmedLine)
		}

		setNestedValue(valuesMap, keyPart, valuePart)
	}

	if scanError := lineScanner.Err(); scanError != nil {
		return nil, fmt.Errorf("error reading values file %s: %w", filePath, scanError)
	}

	return valuesMap, nil
}

// setNestedValue inserts a value into a nested map structure using dot-delimited
// key paths. For example, "web.image" with value "nginx" produces
// {"web": {"image": "nginx"}}. Intermediate maps are created as needed. If an
// intermediate key already exists but is not a map, it is overwritten with a
// new map to accommodate the nested path.
func setNestedValue(targetMap map[string]any, dottedKey string, value any) {
	keySegments := strings.Split(dottedKey, ".")
	currentMap := targetMap

	// Walk through all segments except the last one, creating intermediate maps.
	for _, segment := range keySegments[:len(keySegments)-1] {
		existingValue, exists := currentMap[segment]
		if exists {
			nestedMap, isMap := existingValue.(map[string]any)
			if isMap {
				currentMap = nestedMap
				continue
			}
		}
		// Create a new intermediate map for this segment.
		newNestedMap := make(map[string]any)
		currentMap[segment] = newNestedMap
		currentMap = newNestedMap
	}

	// Set the final segment to the value.
	finalSegment := keySegments[len(keySegments)-1]
	currentMap[finalSegment] = value
}

// MergeValues deep-merges multiple value maps into a single result map. Later
// layers override earlier ones. When both the existing and incoming values for
// a key are maps, they are merged recursively rather than replaced wholesale.
// Non-map values are simply overwritten by later layers.
func MergeValues(layers ...map[string]any) map[string]any {
	resultMap := make(map[string]any)
	for _, layer := range layers {
		deepMergeInto(resultMap, layer)
	}
	return resultMap
}

// deepMergeInto recursively copies all entries from the source map into the
// destination map. When both destination and source contain a map for the same
// key, the maps are merged recursively. Otherwise the source value overwrites
// the destination value.
func deepMergeInto(destination map[string]any, source map[string]any) {
	for sourceKey, sourceValue := range source {
		existingValue, existsInDest := destination[sourceKey]
		if !existsInDest {
			destination[sourceKey] = deepCopyValue(sourceValue)
			continue
		}

		// If both are maps, merge recursively.
		existingMap, existingIsMap := existingValue.(map[string]any)
		sourceMap, sourceIsMap := sourceValue.(map[string]any)
		if existingIsMap && sourceIsMap {
			deepMergeInto(existingMap, sourceMap)
			continue
		}

		// Otherwise, later value wins.
		destination[sourceKey] = deepCopyValue(sourceValue)
	}
}

// deepCopyValue returns a deep copy of the given value. Maps are recursively
// copied to prevent aliasing between the original and the merged result.
// Non-map values (strings, numbers, etc.) are returned as-is since they are
// immutable or value types.
func deepCopyValue(value any) any {
	sourceMap, isMap := value.(map[string]any)
	if !isMap {
		return value
	}
	copiedMap := make(map[string]any, len(sourceMap))
	for mapKey, mapValue := range sourceMap {
		copiedMap[mapKey] = deepCopyValue(mapValue)
	}
	return copiedMap
}

// ParseSetOverrides parses a slice of --set flag values in the form "key=value"
// into a values map. Dot-path notation is supported: "web.instances=10" produces
// {"web": {"instances": "10"}}. Returns an error if any entry is missing the
// equals sign delimiter.
func ParseSetOverrides(setFlags []string) (map[string]any, error) {
	overrideMap := make(map[string]any)
	for _, flagEntry := range setFlags {
		equalsIndex := strings.IndexByte(flagEntry, '=')
		if equalsIndex < 0 {
			return nil, fmt.Errorf("invalid --set flag %q: expected key=value format", flagEntry)
		}
		keyPart := flagEntry[:equalsIndex]
		valuePart := flagEntry[equalsIndex+1:]
		if keyPart == "" {
			return nil, fmt.Errorf("invalid --set flag %q: key cannot be empty", flagEntry)
		}
		setNestedValue(overrideMap, keyPart, valuePart)
	}
	return overrideMap, nil
}

// ParseSetFromEnvOverrides parses a slice of --set-from-env flag values. Each
// entry has the form "key=ENV_VAR" where key is the template value name and
// ENV_VAR is the environment variable to read. The resulting map uses the key
// as entry name and the environment variable's value as the entry value.
// Returns an error if the format is invalid or if the environment variable
// is not set.
func ParseSetFromEnvOverrides(envFlags []string) (map[string]any, error) {
	envOverrideMap := make(map[string]any)
	for _, flagEntry := range envFlags {
		equalsIndex := strings.IndexByte(flagEntry, '=')
		if equalsIndex < 0 {
			return nil, fmt.Errorf("invalid --set-from-env flag %q: expected key=ENV_VAR format", flagEntry)
		}
		keyPart := flagEntry[:equalsIndex]
		envVarName := flagEntry[equalsIndex+1:]
		if keyPart == "" {
			return nil, fmt.Errorf("invalid --set-from-env flag %q: key cannot be empty", flagEntry)
		}
		if envVarName == "" {
			return nil, fmt.Errorf("invalid --set-from-env flag %q: environment variable name cannot be empty", flagEntry)
		}
		envVarValue, isSet := os.LookupEnv(envVarName)
		if !isSet {
			return nil, fmt.Errorf("environment variable %q is not set (required by --set-from-env)", envVarName)
		}
		setNestedValue(envOverrideMap, keyPart, envVarValue)
	}
	return envOverrideMap, nil
}

// RenderWithValuesFiles is the main entry point for the CLI's template rendering
// pipeline. It loads all values files in order, deep-merges them, applies
// --set overrides (which take precedence over file values), applies
// --set-from-env overrides (which take precedence over --set), and finally
// renders the template with the fully merged values map.
func RenderWithValuesFiles(
	templateContent string,
	valuesFilePaths []string,
	setOverrides []string,
	envOverrides []string,
) (string, error) {
	// Collect all value layers in precedence order (earliest = lowest priority).
	var valueLayers []map[string]any

	// Layer 1: values files, in the order specified (later files override earlier).
	for _, filePath := range valuesFilePaths {
		fileValues, loadError := LoadValuesFile(filePath)
		if loadError != nil {
			return "", loadError
		}
		valueLayers = append(valueLayers, fileValues)
	}

	// Layer 2: --set overrides take precedence over file values.
	if len(setOverrides) > 0 {
		setValues, parseError := ParseSetOverrides(setOverrides)
		if parseError != nil {
			return "", parseError
		}
		valueLayers = append(valueLayers, setValues)
	}

	// Layer 3: --set-from-env overrides take highest precedence.
	if len(envOverrides) > 0 {
		envValues, parseError := ParseSetFromEnvOverrides(envOverrides)
		if parseError != nil {
			return "", parseError
		}
		valueLayers = append(valueLayers, envValues)
	}

	// Merge all layers into a single values map.
	mergedValues := MergeValues(valueLayers...)

	// Validate template syntax before rendering to provide clear error messages.
	if syntaxError := ValidateTemplateSyntax(templateContent); syntaxError != nil {
		return "", syntaxError
	}

	// Render the template with the merged values.
	return RenderTemplate(templateContent, mergedValues)
}

// CollectDSLFiles finds all .cca and .ccattler files in the given directory
// and returns their paths sorted alphabetically. Returns an error if no DSL
// files are found or if the glob operation fails.
func CollectDSLFiles(directoryPath string) ([]string, error) {
	ccaFiles, ccaGlobError := filepath.Glob(filepath.Join(directoryPath, "*.cca"))
	if ccaGlobError != nil {
		return nil, fmt.Errorf("scanning for .cca files in %s: %w", directoryPath, ccaGlobError)
	}

	ccattlerFiles, ccattlerGlobError := filepath.Glob(filepath.Join(directoryPath, "*.ccattler"))
	if ccattlerGlobError != nil {
		return nil, fmt.Errorf("scanning for .ccattler files in %s: %w", directoryPath, ccattlerGlobError)
	}

	allDSLFiles := make([]string, 0, len(ccaFiles)+len(ccattlerFiles))
	allDSLFiles = append(allDSLFiles, ccaFiles...)
	allDSLFiles = append(allDSLFiles, ccattlerFiles...)
	sort.Strings(allDSLFiles)

	if len(allDSLFiles) == 0 {
		return nil, fmt.Errorf("no .cca or .ccattler files found in %s", directoryPath)
	}

	return allDSLFiles, nil
}

// ValidateTemplateSyntax performs a parse-only check on the given template
// content to catch syntax errors early, before rendering. This provides
// clearer error messages than discovering syntax problems during rendering.
func ValidateTemplateSyntax(templateContent string) error {
	_, parseError := template.New("syntax-check").
		Funcs(templateFunctions()).
		Parse(templateContent)
	if parseError != nil {
		return fmt.Errorf("template syntax error: %w", parseError)
	}
	return nil
}

// ValidateRenderedDSL parses and compiles the rendered DSL content to verify
// it is valid CCattler DSL. Returns a descriptive error if the rendered output
// contains parse or compilation errors.
func ValidateRenderedDSL(renderedContent string) error {
	parsedFile, parseError := Parse(renderedContent)
	if parseError != nil {
		return fmt.Errorf("rendered DSL parse error: %w", parseError)
	}
	sourceLines := splitSourceLines(renderedContent)
	_, compileError := CompileWithSource(parsedFile, sourceLines)
	if compileError != nil {
		return fmt.Errorf("rendered DSL compile error: %w", compileError)
	}
	return nil
}

// templateActionPattern matches Go template action blocks ({{ ... }}) including
// those spanning multiple lines. Used by DetectUnusedValues to extract action text.
var templateActionPattern = regexp.MustCompile(`(?s)\{\{.*?\}\}`)

// templateKeyReferencePattern matches dot-prefixed identifiers within template
// actions (e.g., .image, .web from .web.image). The captured group is the
// top-level key name.
var templateKeyReferencePattern = regexp.MustCompile(`\.(\w+)`)

// DetectUnusedValues scans the template content for top-level value references
// (patterns like .KeyName within {{ }} actions) and returns a sorted list of
// top-level keys in mergedValues that are not referenced by any template action.
// This is a heuristic: nested access like .web.image is detected as referencing
// the top-level key "web". The result helps warn users about likely-unused values.
func DetectUnusedValues(templateContent string, mergedValues map[string]any) []string {
	// Extract all template action blocks.
	templateActions := templateActionPattern.FindAllString(templateContent, -1)

	// Within actions, find all .KeyName references and collect top-level keys.
	referencedTopLevelKeys := make(map[string]bool)
	for _, actionText := range templateActions {
		keyMatches := templateKeyReferencePattern.FindAllStringSubmatch(actionText, -1)
		for _, keyMatch := range keyMatches {
			referencedTopLevelKeys[keyMatch[1]] = true
		}
	}

	// Identify top-level keys in mergedValues that were not referenced.
	var unusedKeyNames []string
	for topLevelKey := range mergedValues {
		if !referencedTopLevelKeys[topLevelKey] {
			unusedKeyNames = append(unusedKeyNames, topLevelKey)
		}
	}
	sort.Strings(unusedKeyNames)
	return unusedKeyNames
}
