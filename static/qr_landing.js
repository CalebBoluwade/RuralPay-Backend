document.addEventListener('DOMContentLoaded', function () {
  var token = new URLSearchParams(window.location.search).get('token') || '';

  document.querySelectorAll('.bank-btn').forEach(function (btn) {
    var scheme = btn.dataset.scheme;
    var fallback = btn.dataset.fallback;

    btn.addEventListener('click', function (e) {
      e.preventDefault();
      window.location.href = scheme + '://' + (btn.dataset.route || 'pay/qr') + '?token=' + encodeURIComponent(token) + '&source=ruralpay';
      setTimeout(function () {
        if (fallback) window.location.href = fallback;
      }, 1800);
    });

    var img = btn.querySelector('img');
    if (img) {
      img.addEventListener('error', function () {
        img.src = '/static/bank-logos/demo.svg';
      });
    }
  });
});
