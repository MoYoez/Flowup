package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

type StepValue struct {
	Status string
	Output any
}

type Context struct {
	Inputs map[string]any
	Steps  map[string]StepValue
}

type ReferenceKind string

const (
	ReferenceInput  ReferenceKind = "input"
	ReferenceStep   ReferenceKind = "step"
	ReferenceSecret ReferenceKind = "secret"
)

type Reference struct {
	Kind ReferenceKind
	Name string
	Path string
}

var embeddedExpressionPattern = regexp.MustCompile(`\$\{\{\s*(.*?)\s*\}\}`)
var referencePattern = regexp.MustCompile(`\b(?:inputs\.[A-Za-z_][A-Za-z0-9_-]*|steps\.[A-Za-z_][A-Za-z0-9_-]*\.(?:status|output(?:\.[A-Za-z_][A-Za-z0-9_-]*)*)|secrets\.[A-Za-z_][A-Za-z0-9_]*)\b`)

func Resolve(value any, context Context) (any, error) {
	switch typed := value.(type) {
	case string:
		return resolveString(typed, context)
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			resolved, err := Resolve(item, context)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			result[key] = resolved
		}
		return result, nil
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			resolved, err := Resolve(item, context)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", index, err)
			}
			result[index] = resolved
		}
		return result, nil
	default:
		return value, nil
	}
}

func resolveString(source string, context Context) (any, error) {
	if ref, ok, err := ParseSecretReference(source); err != nil {
		return nil, err
	} else if ok {
		return ref, nil
	}
	matches := embeddedExpressionPattern.FindAllStringSubmatchIndex(source, -1)
	if len(matches) == 0 {
		return source, nil
	}
	if len(matches) == 1 && matches[0][0] == 0 && matches[0][1] == len(source) {
		expression := strings.TrimSpace(source[matches[0][2]:matches[0][3]])
		ref, err := parseReference(expression)
		if err != nil {
			return nil, err
		}
		return resolveReference(ref, context)
	}

	var builder strings.Builder
	offset := 0
	for _, match := range matches {
		builder.WriteString(source[offset:match[0]])
		expression := strings.TrimSpace(source[match[2]:match[3]])
		ref, err := parseReference(expression)
		if err != nil {
			return nil, err
		}
		resolved, err := resolveReference(ref, context)
		if err != nil {
			return nil, err
		}
		text, err := scalarText(resolved)
		if err != nil {
			return nil, fmt.Errorf("interpolate %q: %w", expression, err)
		}
		builder.WriteString(text)
		offset = match[1]
	}
	builder.WriteString(source[offset:])
	return builder.String(), nil
}

func scalarText(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case bool:
		return strconv.FormatBool(typed), nil
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32), nil
	case int:
		return strconv.Itoa(typed), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case nil:
		return "null", nil
	default:
		return "", fmt.Errorf("value of type %T cannot be interpolated", value)
	}
}

func parseReference(source string) (Reference, error) {
	parts := strings.Split(source, ".")
	if len(parts) == 2 && parts[0] == "inputs" && parts[1] != "" {
		return Reference{Kind: ReferenceInput, Name: parts[1]}, nil
	}
	if len(parts) == 2 && parts[0] == "secrets" && secretNamePattern.MatchString(parts[1]) {
		return Reference{Kind: ReferenceSecret, Name: parts[1]}, nil
	}
	if len(parts) >= 3 && parts[0] == "steps" && parts[1] != "" {
		path := strings.Join(parts[2:], ".")
		if path == "status" || path == "output" || strings.HasPrefix(path, "output.") {
			return Reference{Kind: ReferenceStep, Name: parts[1], Path: path}, nil
		}
	}
	return Reference{}, fmt.Errorf("invalid reference %q", source)
}

func resolveReference(ref Reference, context Context) (any, error) {
	switch ref.Kind {
	case ReferenceInput:
		value, ok := context.Inputs[ref.Name]
		if !ok {
			return nil, fmt.Errorf("input %q is not available", ref.Name)
		}
		return value, nil
	case ReferenceStep:
		step, ok := context.Steps[ref.Name]
		if !ok {
			return nil, fmt.Errorf("step %q is not available", ref.Name)
		}
		if ref.Path == "status" {
			return step.Status, nil
		}
		value := step.Output
		if ref.Path == "output" {
			return value, nil
		}
		for _, segment := range strings.Split(strings.TrimPrefix(ref.Path, "output."), ".") {
			object, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("step %q output path %q is not an object", ref.Name, ref.Path)
			}
			value, ok = object[segment]
			if !ok {
				return nil, fmt.Errorf("step %q output path %q is missing", ref.Name, ref.Path)
			}
		}
		return value, nil
	case ReferenceSecret:
		return SecretRef{Name: ref.Name}, nil
	default:
		return nil, fmt.Errorf("unsupported reference kind %q", ref.Kind)
	}
}

func References(value any) ([]Reference, error) {
	var refs []Reference
	var visit func(any) error
	visit = func(current any) error {
		switch typed := current.(type) {
		case string:
			for _, expression := range embeddedExpressionPattern.FindAllStringSubmatch(typed, -1) {
				for _, source := range referencePattern.FindAllString(expression[1], -1) {
					ref, err := parseReference(source)
					if err != nil {
						return err
					}
					refs = append(refs, ref)
				}
			}
		case map[string]any:
			for _, item := range typed {
				if err := visit(item); err != nil {
					return err
				}
			}
		case []any:
			for _, item := range typed {
				if err := visit(item); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(value); err != nil {
		return nil, err
	}
	return refs, nil
}

func EvaluateCondition(source string, context Context) (bool, error) {
	expression, err := unwrapExpression(source)
	if err != nil {
		return false, err
	}
	parser := conditionParser{lexer: conditionLexer{source: expression}, context: context}
	parser.next()
	value, err := parser.parseOr()
	if err != nil {
		return false, err
	}
	if parser.current.kind != tokenEOF {
		return false, fmt.Errorf("unexpected token %q", parser.current.text)
	}
	result, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("condition returned %T, want boolean", value)
	}
	return result, nil
}

func unwrapExpression(source string) (string, error) {
	trimmed := strings.TrimSpace(source)
	if !strings.HasPrefix(trimmed, "${{") || !strings.HasSuffix(trimmed, "}}") {
		return "", errors.New("condition must use ${{ ... }}")
	}
	return strings.TrimSpace(trimmed[3 : len(trimmed)-2]), nil
}

type tokenKind int

const (
	tokenInvalid tokenKind = iota
	tokenEOF
	tokenReference
	tokenString
	tokenNumber
	tokenTrue
	tokenFalse
	tokenNull
	tokenAnd
	tokenOr
	tokenEqual
	tokenNotEqual
	tokenLeftParen
	tokenRightParen
)

type token struct {
	kind tokenKind
	text string
}

type conditionLexer struct {
	source string
	offset int
}

func (l *conditionLexer) next() token {
	for l.offset < len(l.source) && unicode.IsSpace(rune(l.source[l.offset])) {
		l.offset++
	}
	if l.offset >= len(l.source) {
		return token{kind: tokenEOF}
	}
	remaining := l.source[l.offset:]
	for operator, kind := range map[string]tokenKind{
		"&&": tokenAnd,
		"||": tokenOr,
		"==": tokenEqual,
		"!=": tokenNotEqual,
	} {
		if strings.HasPrefix(remaining, operator) {
			l.offset += len(operator)
			return token{kind: kind, text: operator}
		}
	}
	switch l.source[l.offset] {
	case '(':
		l.offset++
		return token{kind: tokenLeftParen, text: "("}
	case ')':
		l.offset++
		return token{kind: tokenRightParen, text: ")"}
	case '"', '\'':
		return l.readString()
	}
	if isNumberStart(l.source[l.offset]) {
		return l.readNumber()
	}
	if isIdentifierChar(l.source[l.offset]) {
		return l.readIdentifier()
	}
	character := l.source[l.offset]
	l.offset++
	return token{kind: tokenInvalid, text: string(character)}
}

func (l *conditionLexer) readString() token {
	quote := l.source[l.offset]
	start := l.offset
	l.offset++
	for l.offset < len(l.source) {
		if l.source[l.offset] == '\\' {
			l.offset += 2
			continue
		}
		if l.source[l.offset] == quote {
			l.offset++
			return token{kind: tokenString, text: l.source[start:l.offset]}
		}
		l.offset++
	}
	return token{kind: tokenInvalid, text: l.source[start:]}
}

func (l *conditionLexer) readNumber() token {
	start := l.offset
	for l.offset < len(l.source) {
		character := l.source[l.offset]
		if (character < '0' || character > '9') && character != '-' && character != '+' && character != '.' && character != 'e' && character != 'E' {
			break
		}
		l.offset++
	}
	return token{kind: tokenNumber, text: l.source[start:l.offset]}
}

func (l *conditionLexer) readIdentifier() token {
	start := l.offset
	for l.offset < len(l.source) && isIdentifierChar(l.source[l.offset]) {
		l.offset++
	}
	text := l.source[start:l.offset]
	switch text {
	case "true":
		return token{kind: tokenTrue, text: text}
	case "false":
		return token{kind: tokenFalse, text: text}
	case "null":
		return token{kind: tokenNull, text: text}
	default:
		return token{kind: tokenReference, text: text}
	}
}

func isNumberStart(character byte) bool {
	return character == '-' || character == '+' || character == '.' || (character >= '0' && character <= '9')
}

func isIdentifierChar(character byte) bool {
	return character == '.' || character == '_' || character == '-' ||
		(character >= 'a' && character <= 'z') ||
		(character >= 'A' && character <= 'Z') ||
		(character >= '0' && character <= '9')
}

type conditionParser struct {
	lexer   conditionLexer
	context Context
	current token
}

func (p *conditionParser) next() {
	p.current = p.lexer.next()
}

func (p *conditionParser) parseOr() (any, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.current.kind == tokenOr {
		p.next()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		leftBool, rightBool, err := booleanPair(left, right)
		if err != nil {
			return nil, err
		}
		left = leftBool || rightBool
	}
	return left, nil
}

func (p *conditionParser) parseAnd() (any, error) {
	left, err := p.parseEquality()
	if err != nil {
		return nil, err
	}
	for p.current.kind == tokenAnd {
		p.next()
		right, err := p.parseEquality()
		if err != nil {
			return nil, err
		}
		leftBool, rightBool, err := booleanPair(left, right)
		if err != nil {
			return nil, err
		}
		left = leftBool && rightBool
	}
	return left, nil
}

func (p *conditionParser) parseEquality() (any, error) {
	left, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	operator := p.current.kind
	if operator != tokenEqual && operator != tokenNotEqual {
		return left, nil
	}
	p.next()
	right, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	equal := reflect.DeepEqual(normalizeValue(left), normalizeValue(right))
	if operator == tokenNotEqual {
		equal = !equal
	}
	return equal, nil
}

func (p *conditionParser) parsePrimary() (any, error) {
	current := p.current
	switch current.kind {
	case tokenReference:
		p.next()
		ref, err := parseReference(current.text)
		if err != nil {
			return nil, fmt.Errorf("unexpected token %q", current.text)
		}
		return resolveReference(ref, p.context)
	case tokenString:
		p.next()
		if current.text[0] == '\'' {
			return strings.ReplaceAll(current.text[1:len(current.text)-1], `\'`, `'`), nil
		}
		value, err := strconv.Unquote(current.text)
		if err != nil {
			return nil, fmt.Errorf("invalid string %q: %w", current.text, err)
		}
		return value, nil
	case tokenNumber:
		p.next()
		value, err := strconv.ParseFloat(current.text, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid number %q", current.text)
		}
		return value, nil
	case tokenTrue:
		p.next()
		return true, nil
	case tokenFalse:
		p.next()
		return false, nil
	case tokenNull:
		p.next()
		return nil, nil
	case tokenLeftParen:
		p.next()
		value, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.current.kind != tokenRightParen {
			return nil, fmt.Errorf("expected closing parenthesis, got %q", p.current.text)
		}
		p.next()
		return value, nil
	default:
		return nil, fmt.Errorf("unexpected token %q", current.text)
	}
}

func booleanPair(left, right any) (bool, bool, error) {
	leftBool, leftOK := left.(bool)
	rightBool, rightOK := right.(bool)
	if !leftOK || !rightOK {
		return false, false, fmt.Errorf("logical operator requires booleans, got %T and %T", left, right)
	}
	return leftBool, rightBool, nil
}

func normalizeValue(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return value
	}
	return normalized
}
