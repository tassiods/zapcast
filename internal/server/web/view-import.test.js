// view-import.test.js — quote-aware CSV preview splitter. Pure function,
// no DOM needed beyond Vitest's default jsdom env.

import { describe, it, expect } from 'vitest';
import { splitCSVLine } from './view-import.js';

describe('splitCSVLine', () => {
  it('splits a plain comma-separated line', () => {
    expect(splitCSVLine('João,5511999998888,Grupo')).toEqual([
      'João',
      '5511999998888',
      'Grupo',
    ]);
  });

  it('keeps commas inside double quotes in one cell', () => {
    expect(splitCSVLine('"Silva, João",5511999998888')).toEqual([
      'Silva, João',
      '5511999998888',
    ]);
  });

  it('unescapes doubled quotes', () => {
    expect(splitCSVLine('"Diz ""oi""",5511999998888')).toEqual([
      'Diz "oi"',
      '5511999998888',
    ]);
  });

  it('trims whitespace around unquoted cells', () => {
    expect(splitCSVLine('  João  ,  5511999998888 ')).toEqual([
      'João',
      '5511999998888',
    ]);
  });

  it('keeps empty cells', () => {
    expect(splitCSVLine('João,,Grupo')).toEqual(['João', '', 'Grupo']);
  });
});
