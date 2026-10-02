package prompts

import (
	_ "embed"
)

//go:embed classify_v1.txt
var ClassifyV1 string

const VersionV1 = "classify_v1"
