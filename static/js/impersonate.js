// View As / Impersonation
(function() {
    var viewAsUsers = [];

    function initViewAs() {
        fetch('/api/impersonate/status')
            .then(function(r) { return r.json(); })
            .then(function(status) {
                if (!status.isAdmin) return;

                if (status.active) {
                    // Show banner, hide button
                    var banner = document.getElementById('impersonateBanner');
                    var nameEl = document.getElementById('impersonateName');
                    if (banner && nameEl) {
                        nameEl.textContent = status.displayName || status.userId;
                        banner.classList.add('active');
                    }
                } else {
                    // Show View As button
                    var wrapper = document.getElementById('viewAsWrapper');
                    if (wrapper) wrapper.classList.add('visible');

                    // Pre-fetch users list
                    fetch('/api/allowed-users')
                        .then(function(r) { return r.json(); })
                        .then(function(users) {
                            viewAsUsers = users || [];
                            renderViewAsUsers(viewAsUsers);
                        })
                        .catch(function(err) {
                            console.error('Failed to load users for View As:', err);
                        });
                }
            })
            .catch(function(err) {
                console.error('Failed to check impersonation status:', err);
            });
    }

    function renderViewAsUsers(users) {
        var list = document.getElementById('viewAsUserList');
        if (!list) return;
        list.innerHTML = '';
        users.forEach(function(u) {
            var opt = document.createElement('div');
            opt.className = 'view-as-option';
            opt.textContent = u.displayName + ' ';
            var idSpan = document.createElement('span');
            idSpan.className = 'view-as-option-id';
            idSpan.textContent = u.userId;
            opt.appendChild(idSpan);
            opt.onclick = function() { startImpersonation(u.userId); };
            list.appendChild(opt);
        });
    }

    window.toggleViewAsDropdown = function() {
        var dropdown = document.getElementById('viewAsDropdown');
        if (!dropdown) return;
        var isActive = dropdown.classList.contains('active');
        dropdown.classList.toggle('active', !isActive);
        if (!isActive) {
            var search = document.getElementById('viewAsSearch');
            if (search) { search.value = ''; search.focus(); }
            renderViewAsUsers(viewAsUsers);
        }
    };

    window.filterViewAsUsers = function() {
        var query = (document.getElementById('viewAsSearch').value || '').toLowerCase();
        var filtered = viewAsUsers.filter(function(u) {
            return u.displayName.toLowerCase().indexOf(query) !== -1 ||
                   u.userId.toLowerCase().indexOf(query) !== -1;
        });
        renderViewAsUsers(filtered);
    };

    function getCSRFToken() {
        var meta = document.querySelector('meta[name="csrf-token"]');
        return meta ? meta.getAttribute('content') : '';
    }

    function appendCSRFInput(form) {
        var input = document.createElement('input');
        input.type = 'hidden';
        input.name = 'csrf_token';
        input.value = getCSRFToken();
        form.appendChild(input);
    }

    window.startImpersonation = function(userId) {
        var form = document.createElement('form');
        form.method = 'POST';
        form.action = '/admin/impersonate';
        var input = document.createElement('input');
        input.type = 'hidden';
        input.name = 'user_id';
        input.value = userId;
        form.appendChild(input);
        appendCSRFInput(form);
        document.body.appendChild(form);
        form.submit();
    };

    window.stopImpersonation = function() {
        var form = document.createElement('form');
        form.method = 'POST';
        form.action = '/admin/impersonate/stop';
        appendCSRFInput(form);
        document.body.appendChild(form);
        form.submit();
    };

    // Close dropdown when clicking outside
    document.addEventListener('click', function(e) {
        var wrapper = document.getElementById('viewAsWrapper');
        if (wrapper && !wrapper.contains(e.target)) {
            var dropdown = document.getElementById('viewAsDropdown');
            if (dropdown) dropdown.classList.remove('active');
        }
    });

    // Initialize on page load (skip login page)
    if (window.location.pathname !== '/login') {
        if (document.readyState === 'loading') {
            document.addEventListener('DOMContentLoaded', initViewAs);
        } else {
            initViewAs();
        }
    }
})();
