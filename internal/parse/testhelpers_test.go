package parse

import "regexp"

type regexpT = regexp.Regexp

func regexpCompile(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile(pattern)
}
