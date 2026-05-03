// Package specfmt parses and validates Rex spec YAML documents.
//
// The format itself is described by specs/spec-format.yaml — that file
// is the contract this package implements. The Document type and its
// nested types map one-to-one onto the YAML schema; the validator
// package layered on top reports format violations with file path,
// YAML path, category, and message per spec-format.VAL.1.
//
// Parsing is intentionally lossless for known fields and forgiving for
// unknown ones (the validator decides whether unknown top-level keys
// are an error or a warning depending on mode). The Requirement type
// transparently accepts either a plain-string or mapping form per
// spec-format.REQ.4.
package specfmt
