package container

// GleipnirReservedAddr exposes the unexported reserved-address formula to the
// external container_test package, which can import egress without an import
// cycle (an in-package test could not).
var GleipnirReservedAddr = gleipnirReservedAddr
