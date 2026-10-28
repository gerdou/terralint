package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gerdou/terralint/parser/types"
	"github.com/hashicorp/hcl/v2"
)

// TestCase represents a test case for the parser
type TestCase struct {
	Name        string
	FilePath    string
	Description string
	Expected    types.Body
}

func TestParser(t *testing.T) {
	// Define test cases
	testCases := []TestCase{
		{
			Name:        "Simple Terraform",
			FilePath:    "test_files/simple_test.tf",
			Description: "Simple Terraform file with basic constructs",
			Expected:    createSimpleTerraformExpected(),
		},
		{
			Name:        "Complex Module",
			FilePath:    "test_files/complex_terraform_split/01_complex_module.tf",
			Description: "Complex module with nested expressions, conditionals, and for loops",
			Expected:    createComplexModuleExpected(),
		},
		{
			Name:        "Complex Resource",
			FilePath:    "test_files/complex_terraform_split/02_complex_resource.tf",
			Description: "Resource with complex dynamic blocks and for_each",
			Expected:    createComplexResourceExpected(),
		},
		{
			Name:        "Complex Locals",
			FilePath:    "test_files/complex_terraform_split/03_complex_locals.tf",
			Description: "Complex locals with nested expressions",
			Expected:    createComplexLocalsExpected(),
		},
		{
			Name:        "Complex Data Source",
			FilePath:    "test_files/complex_terraform_split/04_complex_data_source.tf",
			Description: "Data source with complex expressions",
			Expected:    createComplexDataSourceExpected(),
		},
		{
			Name:        "Complex Variable",
			FilePath:    "test_files/complex_terraform_split/05_complex_variable.tf",
			Description: "Variable with complex type constraints and validations",
			Expected:    createComplexVariableExpected(),
		},
		{
			Name:        "Complex Output",
			FilePath:    "test_files/complex_terraform_split/06_complex_output.tf",
			Description: "Output with complex expressions",
			Expected:    createComplexOutputExpected(),
		},
		{
			Name:        "Complex Provider",
			FilePath:    "test_files/complex_terraform_split/07_complex_provider.tf",
			Description: "Provider configuration with complex expressions",
			Expected:    createComplexProviderExpected(),
		},
		{
			Name:        "Complex Terraform Config",
			FilePath:    "test_files/complex_terraform_split/08_complex_terraform_config.tf",
			Description: "Terraform configuration with complex expressions",
			Expected:    createComplexTerraformConfigExpected(),
		},
	}

	// Run tests
	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			// Get the full path to the test file
			fullPath, err := filepath.Abs(tc.FilePath)
			if err != nil {
				t.Fatalf("Failed to get absolute path for %s: %v", tc.FilePath, err)
			}

			// Check if the file exists
			if _, err := os.Stat(fullPath); os.IsNotExist(err) {
				t.Fatalf("Test file does not exist: %s", fullPath)
			}

			// Parse the file
			root, err := ParseTerraformFile(fullPath)
			if err != nil {
				t.Fatalf("Failed to parse %s: %v", tc.FilePath, err)
			}

			// Verify the root is not nil
			if root == nil {
				t.Fatalf("Parsed root is nil for %s", tc.FilePath)
			}

			// Compare the parsed structure with the expected structure
			compareStructures(t, tc.Expected, root)
		})
	}
}

// compareStructures recursively compares the expected and actual structures
func compareStructures(t *testing.T, expected, actual types.Body) {
	// Check if types match
	if reflect.TypeOf(expected) != reflect.TypeOf(actual) {
		t.Errorf("Type mismatch: expected %T, got %T", expected, actual)
		return
	}

	// Compare based on type
	switch exp := expected.(type) {
	case *types.Root:
		act, ok := actual.(*types.Root)
		if !ok {
			t.Errorf("Type assertion failed: expected *types.Root, got %T", actual)
			return
		}
		compareRoots(t, exp, act)
	case *types.Block:
		act, ok := actual.(*types.Block)
		if !ok {
			t.Errorf("Type assertion failed: expected *types.Block, got %T", actual)
			return
		}
		compareBlocks(t, exp, act)
	case *types.Attribute:
		act, ok := actual.(*types.Attribute)
		if !ok {
			t.Errorf("Type assertion failed: expected *types.Attribute, got %T", actual)
			return
		}
		compareAttributes(t, exp, act)
	default:
		t.Errorf("Unsupported type for comparison: %T", expected)
	}
}

// compareRoots compares two Root structures
func compareRoots(t *testing.T, expected, actual *types.Root) {
	// Check if the number of children matches
	if len(expected.Children) != len(actual.Children) {
		t.Errorf("Root children count mismatch: expected %d, got %d",
			len(expected.Children), len(actual.Children))
		return
	}

	// Compare each child
	for i, expChild := range expected.Children {
		if i >= len(actual.Children) {
			t.Errorf("Missing child at index %d in actual", i)
			continue
		}
		compareStructures(t, expChild, actual.Children[i])
	}
}

// compareBlocks compares two Block structures
func compareBlocks(t *testing.T, expected, actual *types.Block) {
	// Check if the block type matches
	if expected.Type != actual.Type {
		t.Errorf("Block type mismatch: expected %s, got %s", expected.Type, actual.Type)
	}

	// Check if the block comment matches
	if expected.BlockComment != actual.BlockComment {
		t.Errorf("Block comment mismatch for block %s: expected %q, got %q",
			expected.Type, expected.BlockComment, actual.BlockComment)
	}

	// Check if the inline comment matches
	if expected.InlineComment != actual.InlineComment {
		t.Errorf("Inline comment mismatch for block %s: expected %q, got %q",
			expected.Type, expected.InlineComment, actual.InlineComment)
	}

	// Check if the labels match
	// Special case: if both are empty (nil or empty slice), consider them equal
	if len(expected.Labels) == 0 && len(actual.Labels) == 0 {
		// Both are empty, so they're considered equal
	} else if !reflect.DeepEqual(expected.Labels, actual.Labels) {
		t.Errorf("Block labels mismatch: expected %v (type: %T), got %v (type: %T)",
			expected.Labels, expected.Labels, actual.Labels, actual.Labels)

		// Print more details about the labels
		t.Logf("Expected labels length: %d", len(expected.Labels))
		t.Logf("Actual labels length: %d", len(actual.Labels))

		if len(expected.Labels) > 0 && len(actual.Labels) > 0 {
			t.Logf("First expected label: %q (type: %T)", expected.Labels[0], expected.Labels[0])
			t.Logf("First actual label: %q (type: %T)", actual.Labels[0], actual.Labels[0])
		}
	}

	// Check if the number of children matches
	if len(expected.Children) != len(actual.Children) {
		t.Errorf("Block %s %v children count mismatch: expected %d, got %d",
			expected.Type, expected.Labels, len(expected.Children), len(actual.Children))

		// Log the actual children for debugging
		t.Logf("Actual children:")
		for i, child := range actual.Children {
			switch c := child.(type) {
			case *types.Block:
				t.Logf("  %d: Block %s %v", i, c.Type, c.Labels)
			case *types.Attribute:
				t.Logf("  %d: Attribute %s", i, c.Name)
			default:
				t.Logf("  %d: %T", i, child)
			}
		}
		return
	}

	// Compare each child
	for i, expChild := range expected.Children {
		if i >= len(actual.Children) {
			t.Errorf("Missing child at index %d in actual", i)
			continue
		}
		compareStructures(t, expChild, actual.Children[i])
	}
}

// compareAttributes compares two Attribute structures
func compareAttributes(t *testing.T, expected, actual *types.Attribute) {
	// Check if the attribute name matches
	if expected.Name != actual.Name {
		t.Errorf("Attribute name mismatch: expected %s, got %s", expected.Name, actual.Name)
	}

	// Check if the block comment matches
	if expected.BlockComment != actual.BlockComment {
		t.Errorf("Block comment mismatch for attribute %s: expected %q, got %q",
			expected.Name, expected.BlockComment, actual.BlockComment)
	}

	// Check if the inline comment matches
	if expected.InlineComment != actual.InlineComment {
		t.Errorf("Inline comment mismatch for attribute %s: expected %q, got %q",
			expected.Name, expected.InlineComment, actual.InlineComment)
	}

	// Compare the attribute values
	compareExpressions(t, expected.Name, expected.Value, actual.Value)
}

// compareExpressions compares two expression trees field by field, all the
// way down, Go types included. Source ranges are left out: the expected trees
// do not carry them, and TestFidelity checks them.
func compareExpressions(t *testing.T, name string, expected, actual types.Expression) {
	t.Helper()
	for _, d := range diffValues(name, reflect.ValueOf(&expected).Elem(), reflect.ValueOf(&actual).Elem()) {
		t.Error(d)
	}
}

// diffValues returns one line for each place where exp and act differ, named
// by its path from the compared value.
func diffValues(path string, exp, act reflect.Value) []string {
	if exp.Type() == reflect.TypeOf(hcl.Range{}) {
		return nil
	}
	switch exp.Kind() {
	case reflect.Interface, reflect.Pointer:
		if exp.IsNil() || act.IsNil() {
			if exp.IsNil() != act.IsNil() {
				return []string{fmt.Sprintf("%s: expected %T, got %T", path, exp.Interface(), act.Interface())}
			}
			return nil
		}
		if exp.Elem().Type() != act.Elem().Type() {
			return []string{fmt.Sprintf("%s: expected %T, got %T", path, exp.Elem().Interface(), act.Elem().Interface())}
		}
		return diffValues(path, exp.Elem(), act.Elem())
	case reflect.Struct:
		var diffs []string
		for i := 0; i < exp.NumField(); i++ {
			diffs = append(diffs, diffValues(path+"."+exp.Type().Field(i).Name, exp.Field(i), act.Field(i))...)
		}
		return diffs
	case reflect.Slice:
		if exp.Len() != act.Len() {
			return []string{fmt.Sprintf("%s: expected %d items, got %d", path, exp.Len(), act.Len())}
		}
		var diffs []string
		for i := 0; i < exp.Len(); i++ {
			diffs = append(diffs, diffValues(fmt.Sprintf("%s[%d]", path, i), exp.Index(i), act.Index(i))...)
		}
		return diffs
	default:
		if !reflect.DeepEqual(exp.Interface(), act.Interface()) {
			return []string{fmt.Sprintf("%s: expected %#v, got %#v", path, exp.Interface(), act.Interface())}
		}
		return nil
	}
}

// Helper function to compare string slices
func compareStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i, v := range a {
		if v != b[i] {
			return false
		}
	}
	return true
}
