---
ticket: {{ .Ticket }}
title: {{ .Title }}
status: draft
authors: [{{ range $i, $a := .Authors }}{{ if $i }}, {{ end }}{{ $a }}{{ end }}]
branches: [{{ range $i, $b := .Branches }}{{ if $i }}, {{ end }}{{ $b }}{{ end }}]
prs: []
created: {{ .Now.Format "2006-01-02" }}
updated: {{ .Now.Format "2006-01-02" }}
---

# {{ if .Title }}{{ .Title }}{{ else }}{{ .Ticket }}{{ end }}

## Summary
{{ .Description }}

## Problem / Motivation
_Why does this work need to happen? What is the user/business pain?_

## Proposed Solution
_What will you build, at a functional level? Sketch the approach._

## Inputs / Outputs / Contracts
_Function signatures, data shapes, API contracts, CLI flags._

## Edge Cases & Constraints
_Boundary conditions, error states, performance limits, security considerations._

## Out of Scope
_What this work item explicitly does not cover._

## Acceptance Criteria
- [ ] _Clear, testable condition_
