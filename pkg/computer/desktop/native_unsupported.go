//go:build !darwin

package desktop

func inspectNative() (Readiness, error) { return Readiness{}, ErrUnsupported }

func CurrentApplication() (Application, error) { return Application{}, ErrUnsupported }
