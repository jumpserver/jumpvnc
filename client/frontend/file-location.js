export function fileLocationPath(location, directory = '/') {
  if (!location?.root) return '';
  const root = location.root;
  const separator = /^[A-Za-z]:\\/.test(root) || root.startsWith('\\\\') ? '\\' : '/';
  if (directory === '/') return root;
  const relative = directory.split('/').filter(Boolean).join(separator);
  return root.replace(/[\\/]$/, '') + separator + relative;
}

export function fileLocationNote(location) {
  if (!location?.root) return '上传到当前目录。此连接未提供服务器上的实际路径。';
  return location.home ? '根目录是远程用户的 HOME，文件上传到当前目录。' : '文件上传到当前目录。';
}
