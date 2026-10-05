// Package domain defines the types the service passes around: the enums, the
// request, what the LLM steps return, and the record sent back to the client.
// It is the Go counterpart of app/schemas.py in the Python service.
//
// Go has no Pydantic, so required fields and validation are explicit: the
// request and the LLM result types are decoded with every key required (a
// missing or null field is an error, never a silent zero value) and then
// checked by a Validate method (enum membership, lengths, ranges). The LLM
// layer treats any such error as a bad generation and retries.
//
// The enum types have no UnmarshalJSON of their own. encoding/json needs it on
// a pointer receiver, which would mix with their value-receiver Valid method;
// membership is checked by Validate and ParseEnvironment instead.
package domain
