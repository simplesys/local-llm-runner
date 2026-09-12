package llm_test

import "errors"

// errTest is a sentinel used to check that handler errors travel unchanged.
var errTest = errors.New("test failure")
