package main

// Logger is the interface for what library calls need in a logger
type Logger interface {
	Debugf(format string, args ...interface{})
	Infof(format string, args ...interface{})
}
