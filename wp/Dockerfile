# WordPress image with PostgreSQL support for talking to goopg.
#
# The stock `wordpress` image only ships the mysqli extension. PG4WP
# (wp-content/db.php) reimplements WordPress's mysqli_* calls on top of the
# PHP `pgsql` extension, so we install that extension and drop PG4WP in.
FROM wordpress:php8.3-apache

# PostgreSQL client library + the PHP pgsql extension PG4WP needs.
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends libpq-dev; \
    docker-php-ext-install pgsql; \
    rm -rf /var/lib/apt/lists/*

# Drop the PG4WP connector into the WordPress template. The official
# entrypoint copies /usr/src/wordpress -> /var/www/html on first boot, so the
# drop-in lands in the live wp-content. db.php auto-detects wp-content/pg4wp.
COPY pg4wp /usr/src/wordpress/wp-content/pg4wp
COPY pg4wp/db.php /usr/src/wordpress/wp-content/db.php

# PG4WP writes optional query/error logs here; make it writable by www-data.
RUN mkdir -p /usr/src/wordpress/wp-content/pg4wp/logs \
    && chown -R www-data:www-data /usr/src/wordpress/wp-content/pg4wp

# Must-use plugin: disable core/plugin/theme update checks — wp_version_check's
# database-size probe queries MySQL's information_schema, which PG4WP cannot
# translate (it fatals the wp-admin dashboard).
COPY mu-plugins /usr/src/wordpress/wp-content/mu-plugins
RUN chown -R www-data:www-data /usr/src/wordpress/wp-content/mu-plugins
