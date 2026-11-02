// Package transaction is the SIP transaction layer (RFC 3261 §17), with the transport it sends and receives on.
//
// # Contexts
//
// A context passed to the layer carries the trace and values of the work it is part of. It never cancels or bounds a
// SIP operation, whose lifetime is the protocol's: a transaction cannot be abandoned once started (RFC 3261 §9.1,
// §17), and the layer sends and retransmits until the transaction ends or the layer closes. The contexts the layer
// passes to its handlers are described on Handler and ClientHandler.
//
// # Traces
//
// Each transaction has a span. W3C trace context crosses only the flows Config.TraceContext tells: none leaves on
// another, whatever path a message takes.
package transaction
