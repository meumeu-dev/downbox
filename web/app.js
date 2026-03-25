function downbox() {
    return {
        // Setup wizard
        needsSetup: true,
        wizardStep: 1,
        wizardData: { tunnel: 'none', cloudflaredToken: '', cloudflaredHostname: '', boreServer: '', boreSecret: '', port: 8080, downloadDir: '~/Downloads' },
        wizardTools: {},
        wizardSaving: false,
        wizardError: '',

        // Settings
        settingsData: { tunnel: 'none', cloudflaredToken: '', cloudflaredHostname: '', boreServer: '', boreSecret: '', port: 8080, downloadDir: '~/Downloads' },
        settingsSaving: false,
        settingsSaved: false,

        // App
        tab: 'downloads',
        downloads: [],
        newURL: '',
        aria2Online: false,
        fileList: [],
        currentPath: '',
        breadcrumbs: [],
        status: null,
        publicURL: '',

        // Preview
        showPreview: false,
        previewSrc: '',
        previewType: '',
        previewName: '',

        // Toast
        toastMsg: '',
        toastVisible: false,

        async init() {
            await this.checkSetup();
            if (!this.needsSetup) this.startApp();
        },

        async checkSetup() {
            try {
                const r = await fetch('/api/setup/status');
                const d = await r.json();
                this.needsSetup = d.needsSetup;
                if (this.needsSetup) {
                    const dr = await fetch('/api/setup/defaults');
                    const defaults = await dr.json();
                    this.wizardData.port = defaults.port;
                    this.wizardData.downloadDir = defaults.downloadDir;
                    this.wizardTools = defaults.tools;
                }
            } catch {}
        },

        async saveSetup() {
            this.wizardSaving = true;
            this.wizardError = '';
            try {
                const r = await fetch('/api/setup/save', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(this.wizardData)
                });
                const d = await r.json();
                if (d.ok) {
                    this.needsSetup = false;
                    this.startApp();
                } else {
                    this.wizardError = d.error || 'Save failed';
                }
            } catch (e) {
                this.wizardError = e.message;
            }
            this.wizardSaving = false;
        },

        startApp() {
            this.fetchStatus();
            this.fetchDownloads();
            this.fetchFiles();
            this.fetchShares();
            setInterval(() => this.fetchDownloads(), 2000);
            setInterval(() => this.fetchStatus(), 10000);
        },

        // --- Downloads ---

        async fetchDownloads() {
            try {
                const r = await fetch('/api/downloads');
                const d = await r.json();
                this.downloads = d.downloads || [];
                this.aria2Online = d.aria2_online || false;
            } catch { this.aria2Online = false; }
        },

        async addDownload() {
            const url = this.newURL.trim();
            if (!url) return;
            await fetch('/api/downloads', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ url })
            });
            this.newURL = '';
            setTimeout(() => this.fetchDownloads(), 500);
        },

        async uploadTorrent(e) {
            const file = e.target.files[0];
            if (!file) return;
            const form = new FormData();
            form.append('torrent', file);
            await fetch('/api/downloads', { method: 'POST', body: form });
            e.target.value = '';
            setTimeout(() => this.fetchDownloads(), 500);
        },

        async pauseDownload(gid) {
            await fetch(`/api/downloads/${gid}/pause`, { method: 'POST' });
            setTimeout(() => this.fetchDownloads(), 300);
        },

        async resumeDownload(gid) {
            await fetch(`/api/downloads/${gid}/resume`, { method: 'POST' });
            setTimeout(() => this.fetchDownloads(), 300);
        },

        async removeDownload(gid) {
            if (!confirm('Remove this download?')) return;
            await fetch(`/api/downloads/${gid}`, { method: 'DELETE' });
            setTimeout(() => this.fetchDownloads(), 300);
        },

        // --- Files ---

        async fetchFiles() {
            try {
                const r = await fetch('/api/files?path=' + encodeURIComponent(this.currentPath));
                const data = await r.json();
                this.fileList = Array.isArray(data) ? data : [];
            } catch { this.fileList = []; }
        },

        browseTo(path) {
            this.currentPath = path;
            this.updateBreadcrumbs();
            this.fetchFiles();
        },

        updateBreadcrumbs() {
            if (!this.currentPath) { this.breadcrumbs = []; return; }
            const parts = this.currentPath.split('/');
            this.breadcrumbs = parts.map((name, i) => ({
                name, path: parts.slice(0, i + 1).join('/')
            }));
        },

        uploading: false,
        uploadProgress: 0,

        async uploadFile(e) {
            const file = e.target.files[0];
            if (!file) return;
            this.uploading = true;
            this.uploadProgress = 0;

            const form = new FormData();
            form.append('file', file);
            form.append('path', this.currentPath);

            try {
                const xhr = new XMLHttpRequest();
                xhr.upload.onprogress = (ev) => {
                    if (ev.lengthComputable) this.uploadProgress = Math.round(ev.loaded / ev.total * 100);
                };
                await new Promise((resolve, reject) => {
                    xhr.onload = () => {
                        if (xhr.status >= 200 && xhr.status < 300) resolve(JSON.parse(xhr.responseText));
                        else reject(new Error(xhr.responseText));
                    };
                    xhr.onerror = () => reject(new Error('Upload failed'));
                    xhr.open('POST', '/api/files/upload');
                    xhr.send(form);
                });
                this.toast('Upload complete');
                this.fetchFiles();
            } catch (err) {
                this.toast('Upload failed: ' + err.message);
            }
            this.uploading = false;
            this.uploadProgress = 0;
            e.target.value = '';
        },

        async deleteFile(f) {
            if (!confirm(`Delete "${f.name}"?`)) return;
            await fetch('/api/files?path=' + encodeURIComponent(f.path), { method: 'DELETE' });
            this.fetchFiles();
        },

        previewFile(f) {
            this.previewName = f.name;
            this.previewType = f.type;
            this.previewSrc = '/api/files/download?path=' + encodeURIComponent(f.path) + '&inline=true';
            this.showPreview = true;
        },

        // --- Shares ---

        async getFileShares(path) {
            try {
                const r = await fetch('/api/shares/file?path=' + encodeURIComponent(path));
                return await r.json() || [];
            } catch { return []; }
        },

        async createShare(path, type) {
            try {
                const r = await fetch('/api/shares', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ path, type })
                });
                const data = await r.json();
                if (data.error) { this.toast(data.error); return null; }
                return data;
            } catch { return null; }
        },

        async stopShare(token) {
            await fetch(`/api/shares/${token}`, { method: 'DELETE' });
            this.toast('Share stopped');
        },

        copyLink(link) {
            navigator.clipboard.writeText(link);
            this.toast('Link copied!');
        },

        positionPanel(wrap) {
            const panel = wrap.querySelector('.share-panel');
            if (!panel) return;
            const btn = wrap.querySelector('button');
            const rect = btn.getBoundingClientRect();
            // Position above the button, clamped to viewport
            let top = rect.top - panel.offsetHeight - 6;
            let left = rect.left;
            if (top < 8) top = rect.bottom + 6;
            if (left + panel.offsetWidth > window.innerWidth - 8) {
                left = window.innerWidth - panel.offsetWidth - 8;
            }
            if (left < 8) left = 8;
            panel.style.top = top + 'px';
            panel.style.left = left + 'px';
        },

        // --- Settings ---

        async loadSettings() {
            try {
                const r = await fetch('/api/status');
                const s = await r.json();
                this.settingsData = {
                    port: s.config?.port || 8080,
                    downloadDir: s.config?.downloadDir || '~/Downloads',
                    tunnel: s.config?.tunnel || 'none',
                    cloudflaredToken: s.config?.cloudflaredToken || '',
                    cloudflaredHostname: s.config?.cloudflaredHostname || '',
                    boreServer: s.config?.boreServer || '',
                    boreSecret: s.config?.boreSecret || '',
                };
            } catch {}
        },

        async saveSettings() {
            this.settingsSaving = true;
            this.settingsSaved = false;
            try {
                const r = await fetch('/api/setup/save', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(this.settingsData)
                });
                const d = await r.json();
                if (d.ok) {
                    this.settingsSaved = true;
                    this.fetchStatus();
                    setTimeout(() => { this.settingsSaved = false; }, 3000);
                }
            } catch {}
            this.settingsSaving = false;
        },

        // --- System ---

        async fetchStatus() {
            try {
                const r = await fetch('/api/status');
                this.status = await r.json();
                if (this.status.publicURL) this.publicURL = this.status.publicURL;
                if (this.status.tunnel?.url) this.publicURL = this.status.tunnel.url;
            } catch {}
        },

        toast(msg) {
            this.toastMsg = msg;
            this.toastVisible = true;
            setTimeout(() => { this.toastVisible = false; }, 2000);
        },

        // --- Helpers ---

        dlName(dl) {
            if (dl.bittorrent?.info?.name) return dl.bittorrent.info.name;
            if (dl.files?.[0]?.path) return dl.files[0].path.split('/').pop();
            if (dl.files?.[0]?.uris?.[0]?.uri) {
                try { return new URL(dl.files[0].uris[0].uri).pathname.split('/').pop() || dl.files[0].uris[0].uri; }
                catch { return dl.files[0].uris[0].uri; }
            }
            return dl.gid;
        },

        dlProgress(dl) {
            const total = parseInt(dl.totalLength || 0);
            if (total === 0) return '0';
            return (parseInt(dl.completedLength || 0) / total * 100).toFixed(1);
        },

        formatSize(bytes) {
            if (!bytes || bytes === 0) return '0 B';
            const u = ['B', 'KB', 'MB', 'GB', 'TB'];
            const i = Math.floor(Math.log(bytes) / Math.log(1024));
            return (bytes / Math.pow(1024, i)).toFixed(i > 0 ? 1 : 0) + ' ' + u[i];
        },

        formatDate(ts) {
            if (!ts) return '';
            const d = new Date(ts * 1000);
            const now = new Date();
            if (d.toDateString() === now.toDateString()) return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
            return d.toLocaleDateString([], { month: 'short', day: 'numeric' });
        },

        typeIcon(type) {
            return { video: '\u{1F3AC}', image: '\u{1F5BC}', audio: '\u{1F3B5}', archive: '\u{1F4E6}', torrent: '\u{1F9F2}', subtitle: '\u{1F4DD}', text: '\u{1F4C4}' }[type] || '\u{1F4CE}';
        }
    };
}
