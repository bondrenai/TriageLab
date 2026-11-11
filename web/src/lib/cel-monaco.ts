// Adapted from agentgateway v1.7.0 ui/src/celMonaco.ts (Apache-2.0).
// Modified by TriageLab: ticket schema, per-model source, standard CEL functions,
// comment highlighting, hover ranges, and completion filtering.
// See web/THIRD_PARTY_NOTICES.md and public/licenses/agentgateway.txt.
import type * as Monaco from 'monaco-editor/editor';

import type { TicketSource } from "./types/ticket";
import { ticketCelSchema } from "./ticket-cel-schema";

// Each editor model has its own connector context; registering another editor
// must not change suggestions in an already-open editor.
const sources = new WeakMap<Monaco.editor.ITextModel, TicketSource>();
export function setTicketModelSource(model: Monaco.editor.ITextModel, source: TicketSource) {
  sources.set(model, source);
}

let celConfigured = false;
const schemaIndexes = new Map<TicketSource, CelSchemaIndex>();

export const celLanguage = 'cel';

type CelCompletionEntry = {
	label: string;
	insertText?: string;
	detail: string;
	documentation?: string;
};

type JsonSchemaNode = {
	description?: string;
	type?: string | string[];
	properties?: Record<string, JsonSchemaNode>;
	additionalProperties?: JsonSchemaNode | boolean;
	items?: JsonSchemaNode;
	default?: unknown;
};

type CelSchemaIndex = {
	globals: CelCompletionEntry[];
	propertiesByPath: Record<string, CelCompletionEntry[]>;
};

const celMethods: readonly CelCompletionEntry[] = [
  { label: 'size', insertText: 'size()', detail: 'CEL size' },
  { label: 'contains', insertText: 'contains($1)', detail: 'String contains' },
  { label: 'startsWith', insertText: 'startsWith($1)', detail: 'String prefix' },
  { label: 'endsWith', insertText: 'endsWith($1)', detail: 'String suffix' },
  { label: 'matches', insertText: 'matches($1)', detail: 'RE2 regular expression' },
  ...['all', 'exists', 'exists_one', 'filter', 'map'].map(label => ({
    label, insertText: `${label}(item, $1)`, detail: `CEL collection ${label}`,
  })),
];
const celFunctions: readonly CelCompletionEntry[] = [
  { label: 'has', insertText: 'has($1)', detail: 'Test whether a map key exists' },
  { label: 'size', insertText: 'size($1)', detail: 'Length of a string, list, or map' },
  ...['string', 'bytes', 'double', 'int', 'uint', 'bool', 'duration', 'timestamp', 'type'].map(label => ({
    label, insertText: `${label}($1)`, detail: `CEL ${label}`,
  })),
];

export function configureCelMonaco(monaco: typeof Monaco) {
	if (celConfigured) return;
	celConfigured = true;

	monaco.languages.register({ id: celLanguage });
	monaco.languages.setLanguageConfiguration(celLanguage, {
		brackets: [
			['(', ')'],
			['[', ']'],
			['{', '}']
		],
		autoClosingPairs: [
			{ open: '(', close: ')' },
			{ open: '[', close: ']' },
			{ open: '{', close: '}' },
			{ open: '"', close: '"' },
			{ open: "'", close: "'" }
		],
		surroundingPairs: [
			{ open: '(', close: ')' },
			{ open: '[', close: ']' },
			{ open: '{', close: '}' },
			{ open: '"', close: '"' },
			{ open: "'", close: "'" }
		]
	});
	monaco.languages.setMonarchTokensProvider(celLanguage, {
		keywords: ['true', 'false', 'null', 'in'],
		operators: [
			'==',
			'!=',
			'<=',
			'>=',
			'<',
			'>',
			'&&',
			'||',
			'!',
			'+',
			'-',
			'*',
			'/',
			'%',
			'?',
			':'
		],
		tokenizer: {
			root: [
				[/\/\/.*$/, 'comment'],
				[/[a-zA-Z_]\w*/, { cases: { '@keywords': 'keyword', '@default': 'identifier' } }],
				[/\d+(\.\d+)?/, 'number'],
				[/"([^"\\]|\\.)*$/, 'string.invalid'],
				[/'([^'\\]|\\.)*$/, 'string.invalid'],
				[/"/, 'string', '@string_double'],
				[/'/, 'string', '@string_single'],
				[/[{}()[\]]/, '@brackets'],
				[/[=!<>]=?|&&|\|\||[+\-*/%?:]/, 'operator'],
				[/[;,.]/, 'delimiter']
			],
			string_double: [
				[/[^\\"]+/, 'string'],
				[/\\./, 'string.escape'],
				[/"/, 'string', '@pop']
			],
			string_single: [
				[/[^\\']+/, 'string'],
				[/\\./, 'string.escape'],
				[/'/, 'string', '@pop']
			]
		}
	});

	monaco.languages.registerCompletionItemProvider(celLanguage, {
		triggerCharacters: ['.'],
		async provideCompletionItems(model, position) {
			const schema = await loadCelSchemaIndex(model);
			const prefix = model.getValueInRange({
				startLineNumber: position.lineNumber,
				startColumn: Math.max(1, position.column - 1),
				endLineNumber: position.lineNumber,
				endColumn: position.column
			});
			const linePrefix = model.getValueInRange({
				startLineNumber: position.lineNumber,
				startColumn: 1,
				endLineNumber: position.lineNumber,
				endColumn: position.column
			});
      const tokens = monaco.editor.tokenize(linePrefix, celLanguage)[0];
      const token = tokens?.at(-1);
      if (token && /^(comment|string)/.test(token.type)) return { suggestions: [] };
			const isMemberCompletion = prefix === '.' || /\.\w*$/.test(linePrefix);
			const word = model.getWordUntilPosition(position);
			const range = {
				startLineNumber: position.lineNumber,
				endLineNumber: position.lineNumber,
				startColumn: word.startColumn,
				endColumn: word.endColumn
			};
			const path = isMemberCompletion ? memberPathBeforeDot(linePrefix) : undefined;
			const schemaEntries = path ? (schema.propertiesByPath[path.join('.')] ?? []) : [];
			const methodEntries = isMemberCompletion && path?.join('.') !== 'ticket'
        ? celMethods : [];
			const entries = isMemberCompletion
				? [...schemaEntries, ...methodEntries]
				: [...schema.globals, ...celFunctions];
			const seen = new Set<string>();
			const suggestions = entries
				.filter(entry => {
					if (seen.has(entry.label)) return false;
					seen.add(entry.label);
					return true;
				})
				.map(entry => ({
					label: entry.label,
					kind: isMemberCompletion
						? monaco.languages.CompletionItemKind.Property
						: entry.insertText?.includes('(')
							? monaco.languages.CompletionItemKind.Function
							: monaco.languages.CompletionItemKind.Variable,
					insertText: entry.insertText ?? entry.label,
					insertTextRules: entry.insertText?.includes('$')
						? monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet
						: undefined,
					detail: entry.detail,
					documentation: entry.documentation,
					range,
					sortText: `${entry.insertText?.includes('(') ? '1' : '0'}-${entry.label}`
				}));
			return { suggestions };
		}
	});

	monaco.languages.registerHoverProvider(celLanguage, {
		async provideHover(model, position) {
			const schema = await loadCelSchemaIndex(model);
			const word = model.getWordAtPosition(position);
			if (!word) return null;
			const path = expressionPathAtPosition(model, new monaco.Position(position.lineNumber, word.endColumn));
			const entries =
				path.length > 1
					? (schema.propertiesByPath[path.slice(0, -1).join('.')] ?? [])
					: schema.globals;
			const entry =
				entries.find(candidate => candidate.label === word.word) ??
				celFunctions.find(candidate => candidate.label === word.word) ??
				celMethods.find(candidate => candidate.label === word.word);
			if (!entry?.documentation && !entry?.detail) return null;
			return {
				range: new monaco.Range(
					position.lineNumber,
					word.startColumn,
					position.lineNumber,
					word.endColumn
				),
				contents: [{ value: `\`${entry.label}\`` }, { value: entry.documentation ?? entry.detail }]
			};
		}
	});
}

export const celEditorOptions: Monaco.editor.IStandaloneEditorConstructionOptions = {
	acceptSuggestionOnCommitCharacter: false,
	copyWithSyntaxHighlighting: false,
	fontSize: 13,
	minimap: { enabled: false },
	quickSuggestions: { other: true, comments: false, strings: false },
	renderLineHighlight: 'none',
	scrollBeyondLastLine: false,
	suggest: {
		showClasses: false,
		showColors: false,
		showConstructors: false,
		showDeprecated: false,
		showEnumMembers: true,
		showEnums: true,
		showEvents: false,
		showFiles: false,
		showFolders: false,
		showFunctions: true,
		showInterfaces: false,
		showIssues: false,
		showKeywords: false,
		showMethods: true,
		showModules: false,
		showOperators: false,
		showProperties: true,
		showReferences: false,
		showSnippets: false,
		showStructs: false,
		showTypeParameters: false,
		showUnits: false,
		showUsers: false,
		showValues: false,
		showVariables: true,
		showWords: false
	},
	wordBasedSuggestions: 'off'
};

function memberPathBeforeDot(linePrefix: string) {
	const match = linePrefix.match(/([a-zA-Z_][\w]*(?:\.[a-zA-Z_][\w]*)*)\.\w*$/);
	return match?.[1]?.split('.');
}

function expressionPathAtPosition(model: Monaco.editor.ITextModel, position: Monaco.Position) {
	const linePrefix = model.getValueInRange({
		startLineNumber: position.lineNumber,
		startColumn: 1,
		endLineNumber: position.lineNumber,
		endColumn: position.column
	});
	const match = linePrefix.match(/([a-zA-Z_][\w]*(?:\.[a-zA-Z_][\w]*)*)$/);
	return match?.[1]?.split('.') ?? [];
}

function loadCelSchemaIndex(model: Monaco.editor.ITextModel) {
  const source = sources.get(model) ?? 'github';
  let index = schemaIndexes.get(source);
  if (!index) {
    index = buildSchemaIndex(ticketCelSchema(source));
    schemaIndexes.set(source, index);
  }
  return index;
}

function buildSchemaIndex(schema: JsonSchemaNode): CelSchemaIndex {
	const propertiesByPath: Record<string, CelCompletionEntry[]> = {};
	const globals = Object.entries(schema.properties ?? {}).map(([name, node]) =>
		schemaEntry(name, node)
	);

	const visit = (path: string, node: JsonSchemaNode) => {
		const properties = node.properties ?? {};
		propertiesByPath[path] = Object.entries(properties).map(([name, child]) =>
			schemaEntry(name, child)
		);
		for (const [name, child] of Object.entries(properties)) {
			if (child.properties) visit(`${path}.${name}`, child);
		}
	};

	for (const [name, node] of Object.entries(schema.properties ?? {})) {
		visit(name, node);
	}

	return { globals, propertiesByPath };
}

function schemaEntry(label: string, node: JsonSchemaNode): CelCompletionEntry {
	const type = Array.isArray(node.type)
		? node.type.filter(entry => entry !== 'null').join(' | ')
		: node.type;
	const detail = type ? `CEL ${type}` : 'CEL value';
	return {
		label,
		detail,
		documentation: node.description
	};
}
