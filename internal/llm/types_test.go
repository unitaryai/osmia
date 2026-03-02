package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignatureValidate(t *testing.T) {
	tests := []struct {
		name      string
		sig       Signature
		expectErr bool
	}{
		{
			name: "valid signature",
			sig: Signature{
				Name:         "TestSig",
				Description:  "A test signature",
				InputFields:  []Field{{Name: "input", Type: FieldTypeString}},
				OutputFields: []Field{{Name: "output", Type: FieldTypeString, Required: true}},
			},
			expectErr: false,
		},
		{
			name: "no output fields",
			sig: Signature{
				Name:        "NoOutputs",
				InputFields: []Field{{Name: "input", Type: FieldTypeString}},
			},
			expectErr: true,
		},
		{
			name: "empty name",
			sig: Signature{
				OutputFields: []Field{{Name: "output", Type: FieldTypeString}},
			},
			expectErr: true,
		},
		{
			name: "no input fields is valid",
			sig: Signature{
				Name:         "OutputOnly",
				OutputFields: []Field{{Name: "result", Type: FieldTypeBool}},
			},
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.sig.Validate()
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSignatureFieldNames(t *testing.T) {
	sig := Signature{
		Name: "Test",
		InputFields: []Field{
			{Name: "alpha"},
			{Name: "beta"},
		},
		OutputFields: []Field{
			{Name: "gamma"},
			{Name: "delta"},
		},
	}

	inputNames := sig.InputFieldNames()
	require.Len(t, inputNames, 2)
	assert.Equal(t, "alpha", inputNames[0])
	assert.Equal(t, "beta", inputNames[1])

	outputNames := sig.OutputFieldNames()
	require.Len(t, outputNames, 2)
	assert.Equal(t, "gamma", outputNames[0])
	assert.Equal(t, "delta", outputNames[1])
}

func TestFieldTypes(t *testing.T) {
	// Verify all field types have distinct string values.
	types := []FieldType{
		FieldTypeString,
		FieldTypeInt,
		FieldTypeFloat,
		FieldTypeBool,
		FieldTypeJSON,
	}

	seen := make(map[FieldType]bool)
	for _, ft := range types {
		assert.False(t, seen[ft], "duplicate field type: %s", ft)
		seen[ft] = true
		assert.NotEmpty(t, string(ft))
	}
}
