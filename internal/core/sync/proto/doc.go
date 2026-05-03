// Package proto defines the wire types shared by the sync client and
// server (sync.API.*).
//
// The two sides exchange:
//
//   - Server state: HEAD id, public-key fingerprint, protocol
//     version (sync.API.1).
//   - Event batches: client pushes a contiguous slice of records
//     past a known server cursor; server validates and acknowledges
//     with the new HEAD (sync.API.2).
//   - Event reads: client pulls everything past a cursor as
//     newline-delimited records over Server-Sent Events
//     (sync.API.3).
//
// All payloads are JSON. Timestamps and HLCs use the same shape they
// have on disk (eventlog.HLC.String); no separate wire encoding.
package proto
