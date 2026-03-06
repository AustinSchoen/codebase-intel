package parser

import (
	"context"
	"testing"
)

func TestDartClassWithMethods(t *testing.T) {
	p := New()
	src := []byte(`class UserService {
  String getUser(int id) {
    return findById(id);
  }

  void deleteUser(int id) {}
}
`)
	result, err := p.ParseFile(context.Background(), "test.dart", src, "dart")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	classSym := findSymbol(result, "UserService")
	if classSym == nil {
		t.Fatal("expected to find symbol UserService")
	}
	if classSym.Kind != "class" {
		t.Errorf("expected kind 'class', got %q", classSym.Kind)
	}
	if classSym.Language != "dart" {
		t.Errorf("expected language 'dart', got %q", classSym.Language)
	}

	methodSym := findSymbolQualified(result, "UserService.getUser")
	if methodSym == nil {
		t.Fatal("expected to find symbol UserService.getUser")
	}
	if methodSym.Kind != "method" {
		t.Errorf("expected kind 'method', got %q", methodSym.Kind)
	}

	deleteSym := findSymbolQualified(result, "UserService.deleteUser")
	if deleteSym == nil {
		t.Fatal("expected to find symbol UserService.deleteUser")
	}
}

func TestDartStatelessWidget(t *testing.T) {
	p := New()
	src := []byte(`class MyWidget extends StatelessWidget {
  Widget build(BuildContext context) {
    return Container();
  }
}
`)
	result, err := p.ParseFile(context.Background(), "test.dart", src, "dart")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	classSym := findSymbol(result, "MyWidget")
	if classSym == nil {
		t.Fatal("expected to find symbol MyWidget")
	}
	if classSym.Kind != "class" {
		t.Errorf("expected kind 'class', got %q", classSym.Kind)
	}

	buildSym := findSymbolQualified(result, "MyWidget.build")
	if buildSym == nil {
		t.Fatal("expected to find symbol MyWidget.build")
	}
	if buildSym.Kind != "method" {
		t.Errorf("expected kind 'method', got %q", buildSym.Kind)
	}

	// Should have inherits relationship to StatelessWidget
	if !hasRelWithKind(result, "inherits", "StatelessWidget") {
		t.Error("expected inherits relationship for 'StatelessWidget'")
	}
}

func TestDartImportsExports(t *testing.T) {
	p := New()
	src := []byte(`import 'package:flutter/material.dart';
export 'src/utils.dart';

class Foo {}
`)
	result, err := p.ParseFile(context.Background(), "test.dart", src, "dart")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	if !hasRelWithKind(result, "references", "package:flutter/material.dart") {
		t.Error("expected import relationship for 'package:flutter/material.dart'")
	}
	if !hasRelWithKind(result, "references", "src/utils.dart") {
		t.Error("expected export relationship for 'src/utils.dart'")
	}
}

func TestDartMixinWithOnClause(t *testing.T) {
	p := New()
	src := []byte(`mixin Logging on Service {
  void log(String msg) {}
}
`)
	result, err := p.ParseFile(context.Background(), "test.dart", src, "dart")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	mixinSym := findSymbol(result, "Logging")
	if mixinSym == nil {
		t.Fatal("expected to find symbol Logging")
	}
	if mixinSym.Kind != "class" {
		t.Errorf("expected kind 'class' for mixin, got %q", mixinSym.Kind)
	}

	logSym := findSymbolQualified(result, "Logging.log")
	if logSym == nil {
		t.Fatal("expected to find symbol Logging.log")
	}

	// Should have inherits relationship for the 'on' clause
	if !hasRelWithKind(result, "inherits", "Service") {
		t.Error("expected inherits relationship for 'Service' (on clause)")
	}
}

func TestDartExtension(t *testing.T) {
	p := New()
	src := []byte(`extension StringExt on String {
  String greet() => 'Hello';
}
`)
	result, err := p.ParseFile(context.Background(), "test.dart", src, "dart")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	extSym := findSymbol(result, "StringExt")
	if extSym == nil {
		t.Fatal("expected to find symbol StringExt")
	}
	if extSym.Kind != "class" {
		t.Errorf("expected kind 'class' for extension, got %q", extSym.Kind)
	}

	greetSym := findSymbolQualified(result, "StringExt.greet")
	if greetSym == nil {
		t.Fatal("expected to find symbol StringExt.greet")
	}
}

func TestDartEnum(t *testing.T) {
	p := New()
	src := []byte(`enum Color { red, green, blue }
`)
	result, err := p.ParseFile(context.Background(), "test.dart", src, "dart")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "Color")
	if sym == nil {
		t.Fatal("expected to find symbol Color")
	}
	if sym.Kind != "enum" {
		t.Errorf("expected kind 'enum', got %q", sym.Kind)
	}
	if sym.Language != "dart" {
		t.Errorf("expected language 'dart', got %q", sym.Language)
	}
}

func TestDartTopLevelFunction(t *testing.T) {
	p := New()
	src := []byte(`void main() {
  helper();
}
`)
	result, err := p.ParseFile(context.Background(), "test.dart", src, "dart")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "main")
	if sym == nil {
		t.Fatal("expected to find symbol main")
	}
	if sym.Kind != "function" {
		t.Errorf("expected kind 'function', got %q", sym.Kind)
	}
	if sym.Language != "dart" {
		t.Errorf("expected language 'dart', got %q", sym.Language)
	}
}

func TestDartClassInheritance(t *testing.T) {
	p := New()
	src := []byte(`class Child extends Base with Mixin implements Interface {}
`)
	result, err := p.ParseFile(context.Background(), "test.dart", src, "dart")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "Child")
	if sym == nil {
		t.Fatal("expected to find symbol Child")
	}

	if !hasRelWithKind(result, "inherits", "Base") {
		t.Error("expected inherits relationship for 'Base'")
	}
	if !hasRelWithKind(result, "inherits", "Mixin") {
		t.Error("expected inherits relationship for 'Mixin'")
	}
	if !hasRelWithKind(result, "inherits", "Interface") {
		t.Error("expected inherits relationship for 'Interface'")
	}
}

func TestDartFunctionCall(t *testing.T) {
	p := New()
	src := []byte(`void main() {
  helper();
}
`)
	result, err := p.ParseFile(context.Background(), "test.dart", src, "dart")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	if !hasRelWithKind(result, "calls", "helper") {
		t.Error("expected 'calls' relationship targeting 'helper'")
	}
}

func TestDartConstructor(t *testing.T) {
	p := New()
	src := []byte(`class Foo {
  Foo(int x);
  Foo.named(int y);
}
`)
	result, err := p.ParseFile(context.Background(), "test.dart", src, "dart")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	// Default constructor
	ctorSym := findSymbolQualified(result, "Foo")
	if ctorSym == nil {
		t.Fatal("expected to find constructor symbol Foo")
	}

	// Named constructor
	namedSym := findSymbolQualified(result, "Foo.named")
	if namedSym == nil {
		t.Fatal("expected to find named constructor symbol Foo.named")
	}
	if namedSym.Kind != "method" {
		t.Errorf("expected kind 'method' for named constructor, got %q", namedSym.Kind)
	}
}

func TestDartGetterSetter(t *testing.T) {
	p := New()
	src := []byte(`class Foo {
  int get value => 42;
  set value(int v) {}
}
`)
	result, err := p.ParseFile(context.Background(), "test.dart", src, "dart")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	getterSym := findSymbolQualified(result, "Foo.value")
	if getterSym == nil {
		t.Fatal("expected to find getter/setter symbol Foo.value")
	}
	if getterSym.Kind != "method" {
		t.Errorf("expected kind 'method', got %q", getterSym.Kind)
	}
}

func TestDartTypeAlias(t *testing.T) {
	p := New()
	src := []byte(`typedef IntList = List<int>;
`)
	result, err := p.ParseFile(context.Background(), "test.dart", src, "dart")
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	sym := findSymbol(result, "IntList")
	if sym == nil {
		t.Fatal("expected to find symbol IntList")
	}
	if sym.Kind != "class" {
		t.Errorf("expected kind 'class' for typedef, got %q", sym.Kind)
	}
}

func TestDartSupportsLanguage(t *testing.T) {
	p := New()
	if !p.SupportsLanguage("dart") {
		t.Error("expected parser to support 'dart'")
	}
}
