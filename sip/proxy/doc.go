// Package proxy is a stateful SIP proxy (RFC 3261 §16) on the transaction layer: forwarding, forking, the relay of
// responses, and the dialogs a proxy that record-routes keeps.
//
// # Contexts
//
// A context passed to the proxy carries the trace and values of the request it forwards, never cancellation (see
// sip/transaction). OnReply gets the context the request was forwarded with.
package proxy
