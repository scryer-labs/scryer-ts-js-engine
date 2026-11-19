// Package analysis defines the engine side of Scryer's shared evidence contracts.
package analysis

import "encoding/json"

type CoverageRequest struct {
	Repository   string   `json:"repository"`
	Artifact     string   `json:"artifact,omitempty"`
	TestBinaries []string `json:"testBinaries,omitempty"`
	TestTargets  []string `json:"testTargets,omitempty"`
}

type CoverageReport struct {
	SchemaVersion int             `json:"schemaVersion"`
	Root          string          `json:"root"`
	Engine        string          `json:"engine,omitempty"`
	Status        string          `json:"status"`
	Message       string          `json:"message,omitempty"`
	Runs          []CoverageRun   `json:"runs,omitempty"`
	Coverage      json.RawMessage `json:"coverage,omitempty"`
}

type CoverageRun struct {
	Command       []string          `json:"command,omitempty"`
	Directory     string            `json:"directory,omitempty"`
	Configuration map[string]string `json:"configuration,omitempty"`
	Name          string            `json:"name"`
	Status        string            `json:"status"`
	Profiles      int               `json:"profiles"`
	Message       string            `json:"message,omitempty"`
}

type CoverageEvidence struct {
	Format      string         `json:"format"`
	Version     string         `json:"version"`
	Artifact    string         `json:"artifact"`
	SHA256      string         `json:"sha256"`
	SourceMatch string         `json:"sourceMatch"`
	SourceSHA   string         `json:"sourceSha,omitempty"`
	Granularity string         `json:"granularity"`
	Files       []CoverageFile `json:"files"`
	Notes       []string       `json:"notes"`
}

type CoverageCount struct {
	Covered uint64 `json:"covered"`
	Count   uint64 `json:"count"`
}

type CoverageRegion struct {
	Start ImpactPosition `json:"start"`
	End   ImpactPosition `json:"end"`
	Count uint64         `json:"count"`
}

type CoverageFile struct {
	Regions []CoverageRegion `json:"regions,omitempty"`
	Path    string           `json:"path"`
	Summary struct {
		Lines     *CoverageCount `json:"lines"`
		Functions *CoverageCount `json:"functions"`
		Regions   *CoverageCount `json:"regions"`
	} `json:"summary"`
	LLVMSegments json.RawMessage `json:"llvmSegments,omitempty"`
}
type ImpactPosition struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

type ImpactRange struct {
	File  string         `json:"file"`
	Start ImpactPosition `json:"start"`
	End   ImpactPosition `json:"end"`
}

type ImpactSymbol struct {
	Role          string          `json:"role,omitempty"`
	TestContext   json.RawMessage `json:"testContext,omitempty"`
	ExecutionKind string          `json:"executionKind,omitempty"`
	ID            string          `json:"id"`
	Key           string          `json:"key"`
	Signature     string          `json:"signature"`
	Fingerprint   string          `json:"fingerprint"`
	Source        string          `json:"source"`
	Location      ImpactRange     `json:"location"`
}

type ImpactCall struct {
	Kind       string      `json:"kind,omitempty"`
	Target     string      `json:"targetSymbolId,omitempty"`
	ID         string      `json:"callSiteId"`
	Caller     string      `json:"callerSymbolId"`
	Candidate  string      `json:"candidateSymbolId"`
	Callee     string      `json:"callee"`
	Resolution string      `json:"resolution"`
	Site       ImpactRange `json:"site"`
}

type ImpactContext struct {
	Path        string `json:"path"`
	Fingerprint string `json:"fingerprint"`
}

type ImpactSnapshot struct {
	Resolution json.RawMessage `json:"resolution,omitempty"`
	Contexts   []ImpactContext `json:"contexts"`
	Symbols    []ImpactSymbol  `json:"symbols"`
	Calls      []ImpactCall    `json:"calls"`
	Notes      []string        `json:"notes"`
}

type ImpactFacts struct {
	SchemaVersion int            `json:"schemaVersion"`
	BeforeSHA     string         `json:"beforeSha"`
	AfterSHA      string         `json:"afterSha"`
	Before        ImpactSnapshot `json:"before"`
	After         ImpactSnapshot `json:"after"`
}
