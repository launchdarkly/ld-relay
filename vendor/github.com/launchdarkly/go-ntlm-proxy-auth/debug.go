package ntlm

var debugf = func(format string, a ...interface{}) {} // discard debug

// SetDebugf sets a debugf function for debug output
func SetDebugf(f func(format string, a ...interface{})) {
	debugf = f
}
