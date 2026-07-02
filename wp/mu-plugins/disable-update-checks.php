<?php
/**
 * Plugin Name: Disable update checks (goopg/PG4WP)
 * Description: WordPress's core/plugin/theme update checks call
 * wp_version_check(), whose database-size probe queries MySQL's
 * information_schema.TABLES — a query the PG4WP PostgreSQL connector
 * cannot translate (SelectSQLRewriter throws "Unsupported call to
 * information_schema"), which fatals the wp-admin dashboard. Updates are
 * managed outside the container in this setup, so short-circuit all three
 * update transients and unhook the admin_init probes.
 */

defined('ABSPATH') || exit;

remove_action('admin_init', '_maybe_update_core');
remove_action('admin_init', '_maybe_update_plugins');
remove_action('admin_init', '_maybe_update_themes');
remove_action('wp_version_check', 'wp_version_check');
remove_action('wp_update_plugins', 'wp_update_plugins');
remove_action('wp_update_themes', 'wp_update_themes');

$goopg_stub_updates = static function () {
    return (object) array(
        'updates'         => array(),
        'response'        => array(),
        'translations'    => array(),
        'version_checked' => get_bloginfo('version'),
        'last_checked'    => time(),
    );
};
add_filter('pre_site_transient_update_core', $goopg_stub_updates);
add_filter('pre_site_transient_update_plugins', $goopg_stub_updates);
add_filter('pre_site_transient_update_themes', $goopg_stub_updates);
