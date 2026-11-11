package agent

import (
	"encoding/json"
	"testing"
)

func TestManagedAgentSchemasMeetPinnedStrictOutputContract(t *testing.T) {
	// kagent 1.0.0 CheckStrict requires additionalProperties:false and every
	// object property in required, including objects nested in array items.
	checkStrictSchema := func(t *testing.T, schema map[string]any) {
		t.Helper()
		var check func(map[string]any, string)
		check = func(node map[string]any, path string) {
			t.Helper()
			switch node["type"] {
			case "object":
				properties, ok := node["properties"].(map[string]any)
				if !ok || node["additionalProperties"] != false {
					t.Fatalf("%s does not declare a closed object", path)
				}
				listed, ok := node["required"].([]any)
				if !ok || len(listed) != len(properties) {
					t.Fatalf("%s must require every declared property", path)
				}
				required := map[string]bool{}
				for _, raw := range listed {
					name, ok := raw.(string)
					if !ok || required[name] || properties[name] == nil {
						t.Fatalf("%s has an invalid required property: %v", path, raw)
					}
					required[name] = true
				}
				for name, raw := range properties {
					child, ok := raw.(map[string]any)
					if !ok || !required[name] {
						t.Fatalf("%s/%s lacks a required property schema", path, name)
					}
					check(child, path+"/"+name)
				}
			case "array":
				items, ok := node["items"].(map[string]any)
				if !ok {
					t.Fatalf("%s lacks an item schema", path)
				}
				check(items, path+"/items")
			case "string":
			default:
				t.Fatalf("%s has an unexpected report schema type: %v", path, node["type"])
			}
		}
		check(schema, "outputSchema")
	}
	t.Run("embedded report contract", func(t *testing.T) { checkStrictSchema(t, Schema()) })
	for name, render := range map[string]func() ([]byte, error){
		"default manifest": func() ([]byte, error) { return Manifest("") },
		"custom managed agent": func() ([]byte, error) {
			return RenderDefinition("kagent", Definition{Name: "custom", ModelRef: "triagelab-model"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			data, err := render()
			if err != nil {
				t.Fatal(err)
			}
			var bundle struct {
				Items []struct {
					Kind string
					Spec map[string]any
				}
			}
			if err := json.Unmarshal(data, &bundle); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, item := range bundle.Items {
				template := item.Spec
				if item.Kind == "Agent" {
					if reference, ok := template["template"].(map[string]any); ok {
						template, _ = reference["inline"].(map[string]any)
					}
				}
				schema, ok := template["outputSchema"].(map[string]any)
				if !ok {
					continue
				}
				found = true
				if template["outputSchemaStrict"] != true {
					t.Fatal("managed agent lost strict structured output")
				}
				checkStrictSchema(t, schema)
			}
			if !found {
				t.Fatal("rendered managed agent has no output schema")
			}
		})
	}
}
