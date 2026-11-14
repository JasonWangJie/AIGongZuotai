export function authValidation(mode: 'login' | 'register', input: { email: string; password: string; name: string }): string {
  if (input.email.trim().length > 254 || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(input.email.trim())) return '请输入有效邮箱地址。';
  if (!input.password) return '请输入密码。';
  if (new TextEncoder().encode(input.password).byteLength > 72) return '密码不能超过 72 字节；中文通常占 3 字节。';
  if (mode === 'register') {
    const bytes = new TextEncoder().encode(input.password).byteLength;
    if (bytes < 10 || bytes > 72) return '密码需为 10—72 字节；中文通常占 3 字节。';
    if (!input.name.trim() || Array.from(input.name.trim()).length > 80) return '名称需为 1—80 个字符。';
  }
  return '';
}
