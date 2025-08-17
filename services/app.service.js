"use strict";

const handshake = require('../app/handshake.json');
const page = require('../app/page.json');
const cities = require('../app/cities.json');
const { helper, telebot } = require('../shared');

/**
 * @typedef {import('moleculer').ServiceSchema} ServiceSchema Moleculer's Service Schema
 * @typedef {import('moleculer').Context} Context Moleculer's Context
 */

/** @type {ServiceSchema} */
module.exports = {
	name: "app",

	/**
	 * Actions
	 */
	actions: {
		handshake: {
			visibility: 'published',
			handler(ctx) {
				try {
					const userVersion = helper.number.formatVersion(ctx.meta.version ?? '0.0.0');
					const latestVersion = helper.number.formatVersion(handshake.version_latest ?? '0.0.0');
					const deprecatedVersion = helper.number.formatVersion(handshake.version_deprecated ?? '0.0.0');

					let url_feedback = null;
					let url_update = null;
					let url_place_share = 'https://www.google.com/maps/search/?api=1&query=LAT,LON';
					let url_share;

					if (ctx.meta.flavor == 'myket') {
						url_feedback = `myket://comment?id=${ctx.meta.app}&version=${ctx.meta.version}`;
						url_update = `myket://check-update?id=${ctx.meta.app}`;
					}

					if (ctx.meta.flavor == 'cafebazaar') {
						url_feedback = `bazaar://details?id=${ctx.meta.app}&version=${ctx.meta.version}`;
						url_update = `bazaar://details?id=${ctx.meta.app}`;
					}

					if(ctx.meta.app == 'ir.khodmirza.towaleti') {
						url_share = 'https://khodmirza.ir/towaleti';
					}

					telebot.send(
						`New application opener\nFlavor: ${ctx.meta.flavor}\nVersion: ${ctx.meta.version}\nApp: ${ctx.meta.app}\n#${(ctx.meta.app ?? '').split('.').join('_')}_${(ctx.meta.version ?? '').split('.').join('_')}_${ctx.meta.flavor}`
					);

					return {
						status: 200,
						meta: {},
						data: {
							...handshake,
							page,
							[userVersion < 222 ? 'isDeprecated' : 'is_deprecated']: userVersion < deprecatedVersion,
							[userVersion < 222 ? 'isUpdateAvailable' : 'is_update_available']: userVersion < latestVersion,
							url_feedback,
							url_update,
							url_share,
							url_place_share,
							cities,
						},
					}
				} catch (error) {
					console.error(error);

					return {
						status: 500
					}
				}
			}
		}
	},
}