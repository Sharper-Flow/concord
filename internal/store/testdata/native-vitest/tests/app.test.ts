import { describe, expect, test } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import Value from '../src/lib/Value.svelte';

describe('outer > inner', () => {
  test('renders', () => {
    render(Value);
    expect(screen.getByRole('heading', { name: 'Value 7' })).toBeInTheDocument();
  });
});
describe('outer', () => {
  describe('inner', () => {
    test('renders', () => {
      render(Value);
      expect(screen.getByRole('heading', { name: 'Value 7' })).toBeInTheDocument();
    });
  });
});
