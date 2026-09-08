# Save controls above the form

Set `ModelAdmin.SaveOnTop: true` to repeat the existing save controls above the
parent fields, with the original bottom row after the inline forms. The default
is `false` and retains only the bottom row.

This option applies to the ordinary add/change form, including user change forms.
Specialized user-creation and password forms keep their separate credential
controls; this checkpoint does not add save-and-continue actions to those pages.

Both rows use `submit_row.html` and the same server-computed permissions. View-only
forms have neither row. Save and continue remain available only when the existing
add/change decision permits saving; save and add another still requires add
permission, and delete remains the existing separately authorized link. Readonly
fields do not remove an otherwise permitted object's existing save controls.

This is presentation only: one enclosing form, one CSRF token and one of each
management token, unchanged button names, existing validation/transactions/audit
and redirect behavior. An invalid submitted form keeps both rows and its errors;
showing controls never grants permission to write. Existing responsive styling
and native keyboard-operable buttons apply to both rows, without JavaScript.

Template loaders may override `submit_row.html` for both positions. It receives
only `can_change`, `can_add`, `can_delete` and `delete_url`; add/change checks are
still enforced on POST. The default template uses contextual URL escaping and
contains no form element, hidden token or DOM ID to duplicate.
