/* 独立的密码显隐兜底：在捕获阶段处理，优先于 Bundle 里绑定在按钮上的旧监听器。 */
document.addEventListener('click', function (event) {
  const target = event.target instanceof Element ? event.target : null;
  const button = target && target.closest('.password-toggle');
  if (!button) return;

  const wrapper = button.closest('.password-input-wrap');
  const input = wrapper && wrapper.querySelector('input');
  if (!input) return;

  event.preventDefault();
  event.stopPropagation();
  event.stopImmediatePropagation();

  const visible = input.type === 'password';
  input.type = visible ? 'text' : 'password';
  button.classList.toggle('password-visible', visible);
  button.title = visible ? '隐藏明文' : '显示明文';
  button.setAttribute('aria-label', button.title);
  button.setAttribute('aria-pressed', String(visible));
}, true);
