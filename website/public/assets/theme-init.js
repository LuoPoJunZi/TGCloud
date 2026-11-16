(() => {
  let value;
  try { value = localStorage.getItem('tgcloud-theme'); } catch {}
  if (value !== 'dark' && value !== 'light') {
    value = window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  }
  document.documentElement.dataset.theme = value;
})();
