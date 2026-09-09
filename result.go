package main

// Result is the sole stdout envelope, including CLI and transport errors.
// A nil exit_code means the remote command's outcome is not known.
type Result struct {
	Status    string   `json:"status"`
	ExitCode  *int     `json:"exit_code"`
	Stdout    string   `json:"stdout"`
	Stderr    string   `json:"stderr"`
	Error     *Problem `json:"error"`
	Truncated bool     `json:"truncated"`
	Data      any      `json:"data,omitempty"`
}

type Problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func completed(code int, out, errout string) Result {
	return Result{Status: "completed", ExitCode: &code, Stdout: out, Stderr: errout}
}

func failure(code, message string) Result {
	return Result{Status: "failed", Error: &Problem{Code: code, Message: message}}
}

func success(data any) Result {
	r := completed(0, "", "")
	r.Data = data
	return r
}

func (r Result) OK() bool {
	return r.Error == nil && (r.Status == "running" || (r.ExitCode != nil && *r.ExitCode == 0))
}
