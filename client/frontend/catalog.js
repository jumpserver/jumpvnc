// Pure catalog selection shared by the UI and tests.
export function selectServers(library, view, folderID, query, sort) {
  const folders = new Map(library.folders.map(folder => [folder.id, folder.name]));
  const words = query.trim().toLocaleLowerCase().split(/\s+/).filter(Boolean);
  return library.servers.filter(server => {
    if (view === 'favorites' && !server.favorite) return false;
    if (view === 'folder' && server.folderId !== folderID) return false;
    const profile = server.profile;
    const text = [server.name, profile.host, profile.user, profile.jump,
      folders.get(server.folderId) || '未分类'].join(' ').toLocaleLowerCase();
    return words.every(word => text.includes(word));
  }).sort((a, b) => {
    if (sort === 'recent' && a.lastOpened !== b.lastOpened) return b.lastOpened - a.lastOpened;
    if (sort === 'created' && a.createdAt !== b.createdAt) return b.createdAt - a.createdAt;
    return a.name.localeCompare(b.name, 'zh-CN', { numeric: true }) || a.id.localeCompare(b.id);
  });
}
