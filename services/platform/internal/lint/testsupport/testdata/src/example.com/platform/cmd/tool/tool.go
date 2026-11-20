// Package tool imports a package that only shares a test-support package's name.
package tool

import "example.com/platform/cmd/tool/internal/pipeline/pipelinetest"

var _ pipelinetest.Forge
