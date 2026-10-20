// Package api serves the dashboard's data endpoints.
//
// Two rules from the design shape everything in this package and are worth
// stating before any code:
//
//   - Aggregate only. No endpoint returns message text, a talker, a wxid or a
//     nickname. A conversation is named by report.TalkerHash and by nothing
//     else.
//   - No identity in the output. The talker travels in the URL (?talker=), so
//     neither the request log nor an error body may echo a URL or an error
//     string that could carry one.
package api

import "github.com/gin-gonic/gin"

// errorBody is the only shape an API failure ever takes. The three fields are
// always present, even when hint is empty: a client that has to tell "the
// server did not say" from "the server said nothing useful" is a client that
// will render an empty box next to a spinner.
//
// message is written for a person reading the dashboard, and hint says what to
// do about it. Neither is ever built from err.Error(): an error string is the
// easiest place for a database path, a SQL fragment or — worse — a value that
// came out of the chat log to escape into an HTTP response.
type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

// writeError is the single exit for an error response.
//
// One function rather than a c.JSON call per handler is what makes the rule
// above enforceable by reading: there is exactly one place a status reaches the
// client, so there is exactly one place to look when asking whether an error
// body can carry something it must not.
//
// code is a stable machine-readable token (snake or kebab case, never
// localized); message and hint are for the user and are supplied by the
// caller, never derived from a wrapped error.
func writeError(c *gin.Context, status int, code, message, hint string) {
	c.AbortWithStatusJSON(status, errorBody{
		Code:    code,
		Message: message,
		Hint:    hint,
	})
}
