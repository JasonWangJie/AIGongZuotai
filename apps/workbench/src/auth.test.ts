import { describe, expect, it } from 'vitest';
import { authValidation } from './auth';

describe('account input contract', () => {
  const input = { email: 'creator@example.com', name: '创作者', password: 'long-enough-password' };
  it('accepts valid registration without altering the password', () => { expect(authValidation('register', input)).toBe(''); expect(input.password).toBe('long-enough-password'); });
  it('checks bcrypt byte limits for Chinese and emoji, not just character count', () => { expect(authValidation('register', { ...input, password: '密'.repeat(25) })).toContain('72'); expect(authValidation('register', { ...input, password: '🌙'.repeat(19) })).toContain('72'); expect(authValidation('register', { ...input, password: '密'.repeat(4) })).toBe(''); });
  it('rejects empty names, invalid email and short passwords on register', () => { expect(authValidation('register', { ...input, name: '  ' })).not.toBe(''); expect(authValidation('register', { ...input, email: 'not-email' })).not.toBe(''); expect(authValidation('register', { ...input, password: 'short' })).not.toBe(''); });
  it('does not enforce new registration policy on existing login passwords', () => { expect(authValidation('login', { ...input, password: 'old' })).toBe(''); expect(authValidation('login', { ...input, password: '' })).not.toBe(''); });
});
