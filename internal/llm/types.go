// Package llm provides a DSPy-inspired abstraction for structured LLM
// interactions. It defines typed signatures, composable modules, and a
// budget-aware client that all RoboDev subsystems share.
package llm

import "fmt"

// FieldType describes the expected type of a signature field.
type FieldType string

const (
	// FieldTypeString represents a free-form text field.
	FieldTypeString FieldType = "string"
	// FieldTypeInt represents an integer field.
	FieldTypeInt FieldType = "int"
	// FieldTypeFloat represents a floating-point field.
	FieldTypeFloat FieldType = "float"
	// FieldTypeBool represents a boolean field.
	FieldTypeBool FieldType = "bool"
	// FieldTypeJSON represents an arbitrary JSON object field.
	FieldTypeJSON FieldType = "json"
)

// Field describes a single named input or output of a Signature.
type Field struct {
	// Name is the field identifier used in prompt formatting and response parsing.
	Name string
	// Description explains the field's purpose to the LLM.
	Description string
	// Type constrains the expected value type.
	Type FieldType
	// Required indicates whether the field must be provided (inputs) or
	// guaranteed present (outputs).
	Required bool
}

// Signature defines a typed LLM interaction contract, inspired by DSPy
// signatures. Each signature declares its input and output fields, enabling
// automatic prompt construction and structured response parsing.
type Signature struct {
	// Name identifies this signature (e.g. "ScoreToolCall", "ExtractFacts").
	Name string
	// Description summarises what this signature does, included in the
	// system prompt for context.
	Description string
	// InputFields lists the expected inputs.
	InputFields []Field
	// OutputFields lists the expected outputs.
	OutputFields []Field
}

// Validate checks that the signature has a name and at least one output field.
func (s Signature) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("signature must have a non-empty name")
	}
	if len(s.OutputFields) == 0 {
		return fmt.Errorf("signature %q must have at least one output field", s.Name)
	}
	return nil
}

// InputFieldNames returns the names of all input fields.
func (s Signature) InputFieldNames() []string {
	names := make([]string, len(s.InputFields))
	for i, f := range s.InputFields {
		names[i] = f.Name
	}
	return names
}

// OutputFieldNames returns the names of all output fields.
func (s Signature) OutputFieldNames() []string {
	names := make([]string, len(s.OutputFields))
	for i, f := range s.OutputFields {
		names[i] = f.Name
	}
	return names
}
