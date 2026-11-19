package execution

import (
	"example.com/platform/internal/pipeline/pipelinetest"   // want `example.com/platform/internal/pipeline/pipelinetest is test support`
	"example.com/platform/internal/secretbroker/brokertest" // want `example.com/platform/internal/secretbroker/brokertest is test support`
)

var (
	_ pipelinetest.Forge
	_ brokertest.Broker
)
