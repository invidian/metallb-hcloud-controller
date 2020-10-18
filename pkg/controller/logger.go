package controller

import (
	"io"

	"k8s.io/klog/v2"
)

// Logger is an logging interface used by this controller. Default implementation
// is provided by Klogger struct, however other implementations can be configured
// and used.
type Logger interface {
	// Logger should implement writer interface for writing debug logs.
	io.Writer

	// Errorf should be used for logging non-critical (operational) failures.
	Errorf(format string, a ...interface{})

	// Infof should be used to log details about activity happening.
	Infof(format string, a ...interface{})

	// Debugf should be used to log as much details as possible.
	Debugf(format string, a ...interface{})
}

// Klogger is a default Logger interface implementation using klog.
type Klogger struct{}

// Errorf implements Logger interface using klog.Errorf.
func (k Klogger) Errorf(format string, a ...interface{}) {
	klog.Errorf(format, a...)
}

// Infof implements Logger interface using klog.Infof.
func (k Klogger) Infof(format string, a ...interface{}) {
	klog.Infof(format, a...)
}

// Debugf implements Logger interface using klog.Infof with log level defined by
// klogLevelDebug constant.
func (k Klogger) Debugf(format string, a ...interface{}) {
	klog.V(klogLevelDebug).Infof(format, a...)
}

// Write implements Logger interface using klog.Infof with log level defined by
// klogLevelDebug constant.
func (k Klogger) Write(p []byte) (int, error) {
	klog.V(klogLevelDebug).Infof(string(p))

	return len(p), nil
}
