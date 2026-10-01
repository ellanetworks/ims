// Package sip is the SIP message layer of Ella IMS (RFC 3261 §7, §19, §20,
// §25): parsing, serialization, typed views of the core header fields,
// request and response validation, and the construction helpers that a
// user agent or a proxy needs.
//
// Messages are lossless. Header fields are kept as a list in wire order,
// with their names as received and their values unfolded but otherwise
// untouched. Typed views (Via, From, CSeq, ...) are parsed on demand from
// those values, so a proxy forwards what it does not understand unchanged,
// and an unmodified message serializes to the bytes it was parsed from.
//
// The package knows nothing about IMS. P-headers, Security-* and
// Authorization values are left for the IMS components to parse.
//
// # Copies
//
// URI, Via, Address and Params are values: changing a copy through
// Params.Set or Params.Del never changes the original. Header and the
// messages that hold one share storage when copied: use Clone first.
//
// # Errors
//
// Errors returned by Parse, StreamReader.Next and Validate start with
// "sip: " and wrap their cause, so errors.Is and errors.As find the
// sentinels (ErrMissingHeader, ErrUnanswerable, ErrMissingContentLength,
// ErrMessageTooLarge) and the types (*ParseError, *StatusError). The
// typed accessors and the Parse functions for single values (ParseURI,
// ParseVia, ...) return errors that name what failed, without the prefix,
// so that they read well once wrapped.
package sip
