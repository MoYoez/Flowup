package workflow

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseRejectsLegacyPipelineFormat(t *testing.T) {
	_, err := Parse([]byte(`
pipeline: old
kind: agent
profile: default
tools: [http]
nodes: []
`))

	require.Error(t, err)
}
