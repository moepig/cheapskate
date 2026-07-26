package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSelectorValidate(t *testing.T) {
	cases := []struct {
		name string
		sel  Selector
		ok   bool
	}{
		{"valid", Selector{TagKey: "env", TagValue: "dev", Types: []ResourceType{TypeRdsInstance}}, true},
		{"missing key", Selector{TagValue: "dev", Types: []ResourceType{TypeRdsInstance}}, false},
		{"key too long", Selector{TagKey: string(make([]byte, 129)), TagValue: "dev", Types: []ResourceType{TypeRdsInstance}}, false},
		{"reserved aws prefix", Selector{TagKey: "aws:env", TagValue: "dev", Types: []ResourceType{TypeRdsInstance}}, false},
		{"missing value", Selector{TagKey: "env", Types: []ResourceType{TypeRdsInstance}}, false},
		{"value too long", Selector{TagKey: "env", TagValue: string(make([]byte, 257)), Types: []ResourceType{TypeRdsInstance}}, false},
		{"no types", Selector{TagKey: "env", TagValue: "dev"}, false},
		{"unknown type", Selector{TagKey: "env", TagValue: "dev", Types: []ResourceType{"sqs-queue"}}, false},
		{"multiple known types", Selector{TagKey: "env", TagValue: "dev", Types: []ResourceType{TypeEcsService, TypeRdsCluster, TypeEc2Instance}}, true},
	}
	for _, c := range cases {
		err := c.sel.Validate()
		if c.ok {
			assert.NoErrorf(t, err, c.name)
		} else {
			assert.Errorf(t, err, c.name)
		}
	}
}

func TestSelectorEmpty(t *testing.T) {
	assert.True(t, Selector{}.Empty())
	assert.False(t, Selector{TagKey: "env"}.Empty())
	assert.False(t, Selector{TagValue: "dev"}.Empty())
	assert.False(t, Selector{Types: []ResourceType{TypeRdsInstance}}.Empty())
}
