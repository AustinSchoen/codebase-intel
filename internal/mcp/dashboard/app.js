(function() {
    'use strict';

    const REFRESH_INTERVAL = 30000;
    let refreshTimer = null;

    function formatNumber(n) {
        if (n == null) return '0';
        if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M';
        if (n >= 1000) return (n / 1000).toFixed(1) + 'K';
        return n.toLocaleString();
    }

    function formatDuration(ms) {
        if (!ms) return '-';
        if (ms < 1000) return ms + 'ms';
        if (ms < 60000) return (ms / 1000).toFixed(1) + 's';
        return (ms / 60000).toFixed(1) + 'm';
    }

    function formatUptime(secs) {
        if (!secs) return '-';
        var d = Math.floor(secs / 86400);
        var h = Math.floor((secs % 86400) / 3600);
        var m = Math.floor((secs % 3600) / 60);
        var parts = [];
        if (d > 0) parts.push(d + 'd');
        if (h > 0) parts.push(h + 'h');
        parts.push(m + 'm');
        return parts.join(' ');
    }

    function timeAgo(isoString) {
        if (!isoString) return 'never';
        var secs = (Date.now() - new Date(isoString).getTime()) / 1000;
        if (secs < 60) return Math.floor(secs) + 's ago';
        if (secs < 3600) return Math.floor(secs / 60) + 'm ago';
        if (secs < 86400) return Math.floor(secs / 3600) + 'h ago';
        return Math.floor(secs / 86400) + 'd ago';
    }

    function freshnessClass(ageSecs) {
        if (!ageSecs) return 'old';
        if (ageSecs < 3600) return 'fresh';       // < 1h
        if (ageSecs < 86400) return 'stale';      // < 24h
        return 'old';
    }

    async function fetchJSON(url) {
        try {
            var resp = await fetch(url);
            if (!resp.ok) return null;
            return await resp.json();
        } catch (e) {
            return null;
        }
    }

    function renderGlobalStats(stats) {
        var el = document.getElementById('global-stats');
        if (!stats) { el.innerHTML = ''; return; }

        var pills = [
            { label: 'Codebases', value: stats.codebase_count || 0 },
            { label: 'Files', value: formatNumber(stats.total_files) },
            { label: 'Symbols', value: formatNumber(stats.total_symbols) },
            { label: 'Vectors', value: formatNumber(stats.total_vectors) },
            { label: 'Relationships', value: formatNumber(stats.total_relationships) },
        ];

        el.innerHTML = pills.map(function(p) {
            return '<div class="pill"><span class="label">' + p.label +
                   '</span><span class="value">' + p.value + '</span></div>';
        }).join('');

        document.getElementById('uptime').textContent = 'Up ' + formatUptime(stats.uptime_secs);
    }

    function renderCodebases(data) {
        var grid = document.getElementById('codebases-grid');
        if (!data || !data.codebases || data.codebases.length === 0) {
            grid.innerHTML = '<div class="empty">No codebases indexed</div>';
            return;
        }

        grid.innerHTML = data.codebases.map(function(cb) {
            var fc = freshnessClass(cb.index_age_secs);
            return '<div class="card">' +
                '<div class="card-header">' +
                    '<span class="card-title">' + escapeHtml(cb.display_name || cb.id) + '</span>' +
                    '<span class="freshness ' + fc + '" title="' + fc + '"></span>' +
                '</div>' +
                '<div class="card-path" title="' + escapeHtml(cb.root_path || '') + '">' +
                    escapeHtml(cb.root_path || cb.id) +
                '</div>' +
                '<div class="card-stats">' +
                    statBox(cb.file_count, 'Files') +
                    statBox(cb.symbol_count, 'Symbols') +
                    statBox(cb.vector_count, 'Vectors') +
                    statBox(cb.relationship_count, 'Relations') +
                '</div>' +
                '<div class="card-footer">' +
                    '<span>Last indexed: ' + (cb.last_indexed_at ? timeAgo(cb.last_indexed_at) : 'never') + '</span>' +
                    '<span>' + formatNumber(cb.chunk_count) + ' chunks</span>' +
                '</div>' +
            '</div>';
        }).join('');
    }

    function statBox(value, label) {
        return '<div class="stat"><span class="stat-value">' +
               formatNumber(value) + '</span><span class="stat-label">' +
               label + '</span></div>';
    }

    function renderIndexers(data) {
        var tbody = document.getElementById('indexers-body');
        if (!data || !data.indexers || data.indexers.length === 0) {
            tbody.innerHTML = '<tr><td colspan="6" class="empty">No indexer nodes connected</td></tr>';
            return;
        }

        tbody.innerHTML = data.indexers.map(function(n) {
            var dotClass = n.online ? 'online' : 'offline';
            return '<tr>' +
                '<td><span class="status-dot ' + dotClass + '"></span></td>' +
                '<td>' + escapeHtml(n.node_id) + '</td>' +
                '<td>' + (n.codebases || []).map(escapeHtml).join(', ') + '</td>' +
                '<td>' + escapeHtml(n.status) + '</td>' +
                '<td>' + timeAgo(n.connected_at) + '</td>' +
                '<td>' + timeAgo(n.last_seen) + '</td>' +
            '</tr>';
        }).join('');
    }

    function renderActivity(data) {
        var tbody = document.getElementById('activity-body');
        if (!data || !data.requests || data.requests.length === 0) {
            tbody.innerHTML = '<tr><td colspan="7" class="empty">No recent reindex activity</td></tr>';
            return;
        }

        tbody.innerHTML = data.requests.map(function(r) {
            var statusClass = r.status || 'requested';
            var typeLabel = r.full ? 'full' : 'incremental';
            var filesText = r.files_processed + '/' + r.files_total;
            if (r.files_indexed > 0) {
                filesText += ' (' + r.files_indexed + ' indexed)';
            }
            return '<tr>' +
                '<td><span class="badge ' + statusClass + '">' + escapeHtml(r.status) + '</span></td>' +
                '<td>' + escapeHtml(r.codebase) + '</td>' +
                '<td><span class="badge ' + typeLabel + '">' + typeLabel + '</span></td>' +
                '<td>' + filesText + '</td>' +
                '<td>' + formatDuration(r.duration_ms) + '</td>' +
                '<td>' + timeAgo(r.started_at) + '</td>' +
                '<td>' + escapeHtml(r.node_id || '-') + '</td>' +
            '</tr>';
        }).join('');
    }

    function escapeHtml(str) {
        if (!str) return '';
        var div = document.createElement('div');
        div.appendChild(document.createTextNode(str));
        return div.innerHTML;
    }

    async function refreshAll() {
        var results = await Promise.all([
            fetchJSON('/api/stats'),
            fetchJSON('/api/codebases'),
            fetchJSON('/api/indexers'),
            fetchJSON('/api/reindex-history')
        ]);

        renderGlobalStats(results[0]);
        renderCodebases(results[1]);
        renderIndexers(results[2]);
        renderActivity(results[3]);

        document.getElementById('last-updated').textContent =
            'Updated ' + new Date().toLocaleTimeString();
    }

    // Initial load
    refreshAll();

    // Auto-refresh
    refreshTimer = setInterval(refreshAll, REFRESH_INTERVAL);
})();
