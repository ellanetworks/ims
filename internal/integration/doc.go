// Package integration holds the end-to-end tests of the IMS: the server runs
// with real IPsec in the test's network namespace, and each test UE in its
// own, joined by veth. Its tests live in their own binary because
// netnstest.Main moves the whole binary into a user namespace.
package integration
