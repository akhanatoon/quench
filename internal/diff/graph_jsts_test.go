package diff

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func TestExpandJSReverseImporters_RelativeImportsAndOneHop(t *testing.T) {
	dir := t.TempDir()

	writeTestFile(t, dir, "src/math.ts", `export const add = (a: number, b: number) => a + b;`)
	writeTestFile(t, dir, "src/calc.ts", `import { add } from './math';
export const double = (x: number) => add(x, x);`)
	writeTestFile(t, dir, "src/reexport.ts", `export { add } from './math';`)
	writeTestFile(t, dir, "src/legacy.cjs", `const { add } = require('./math');
module.exports = { add };`)
	writeTestFile(t, dir, "src/nested/consumer.tsx", `import {
  add,
} from '../math';
export const View = () => add(1, 2);`)
	// 2-hop importer should NOT be included
	writeTestFile(t, dir, "src/app.ts", `import { double } from './calc';
console.log(double(2));`)
	// Unrelated file should NOT be included
	writeTestFile(t, dir, "src/other.ts", `import React from 'react';
export const x = 1;`)

	got := ExpandJSReverseImporters(dir, []string{"src/math.ts"})
	want := []string{
		"src/calc.ts",
		"src/legacy.cjs",
		"src/math.ts",
		"src/nested/consumer.tsx",
		"src/reexport.ts",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExpandJSReverseImporters() =\n%v\nwant:\n%v", got, want)
	}
}

func TestExpandJSReverseImporters_TSESMJsToTsResolution(t *testing.T) {
	dir := t.TempDir()

	writeTestFile(t, dir, "src/utils.ts", `export function fmt(v: string) { return v.trim(); }`)
	writeTestFile(t, dir, "src/Button.tsx", `export function Button() { return null; }`)
	writeTestFile(t, dir, "src/service.mts", `import { fmt } from './utils.js';
export const run = () => fmt('ok');`)
	writeTestFile(t, dir, "src/page.ts", `import { Button } from './Button.js';
export const Page = Button;`)

	got := ExpandJSReverseImporters(dir, []string{"src/utils.ts", "src/Button.tsx"})
	want := []string{
		"src/Button.tsx",
		"src/page.ts",
		"src/service.mts",
		"src/utils.ts",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExpandJSReverseImporters() =\n%v\nwant:\n%v", got, want)
	}
}

func TestExpandJSReverseImporters_BarrelIndexResolution(t *testing.T) {
	dir := t.TempDir()

	writeTestFile(t, dir, "src/components/index.ts", `export const Card = () => null;`)
	writeTestFile(t, dir, "src/App.tsx", `import { Card } from './components';
export const App = () => Card();`)
	writeTestFile(t, dir, "src/components/sibling.ts", `import { Card } from '.';
export const Sibling = Card;`)

	got := ExpandJSReverseImporters(dir, []string{"src/components/index.ts"})
	want := []string{
		"src/App.tsx",
		"src/components/index.ts",
		"src/components/sibling.ts",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExpandJSReverseImporters() =\n%v\nwant:\n%v", got, want)
	}
}

func TestExpandJSReverseImporters_SkipsIgnoredDirs(t *testing.T) {
	dir := t.TempDir()

	writeTestFile(t, dir, "src/core.ts", `export const core = 1;`)
	writeTestFile(t, dir, "src/main.ts", `import { core } from './core';`)
	writeTestFile(t, dir, "node_modules/pkg/index.js", `const { core } = require('../../src/core');`)
	writeTestFile(t, dir, "dist/main.js", `import { core } from '../src/core';`)
	writeTestFile(t, dir, "build/main.js", `import { core } from '../src/core';`)
	writeTestFile(t, dir, ".next/chunk.js", `import { core } from '../src/core';`)
	writeTestFile(t, dir, "coverage/report.js", `import { core } from '../src/core';`)
	writeTestFile(t, dir, "vendor/ext.js", `import { core } from '../src/core';`)

	got := ExpandJSReverseImporters(dir, []string{"src/core.ts"})
	want := []string{
		"src/core.ts",
		"src/main.ts",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExpandJSReverseImporters() =\n%v\nwant:\n%v", got, want)
	}
}

func TestExpandJSReverseImporters_EmptyInput(t *testing.T) {
	dir := t.TempDir()
	if got := ExpandJSReverseImporters(dir, nil); got != nil {
		t.Fatalf("expected nil for empty changedFiles, got %v", got)
	}
}
