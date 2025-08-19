"use strict";

const ad = require('../app/ad.json');

/**
 * @typedef {import('moleculer').ServiceSchema} ServiceSchema Moleculer's Service Schema
 * @typedef {import('moleculer').Context} Context Moleculer's Context
 */

/** @type {ServiceSchema} */
module.exports = {
	name: "ad",

	/**
	 * Actions
	 */
	actions: {
		one: {
			visibility: 'published',
			params: {
				type: {
					type: 'enum',
					values: ['any', 'city'],
					default: 'any'
				},
				city: {
					type: "string",
					optional: true,
					default: null
				}
			},
			cache: {
				enabled: true,
				keys: ['type', 'city'],
				ttl: 60 * 5
			},
			async handler(ctx) {
				try {
					if(ad.length == 0) {
						return {
							status: 404,
							message: 'NO_AD'
						}
					}

					const { type, city } = ctx.params;

					let hasCity = city != null && city.length != 0;

					if (type == 'any' && !hasCity) {
						const ads = ad.filter((item) => item.cities.length == 0);

						// random ad
						if (ads.length > 0) {
							const randomAd = ads[Math.floor(Math.random() * ads.length)];

							return {
								status: 200,
								meta: {},
								ad: randomAd
							};
						}

						return {
							status: 404,
							message: 'AD_NOT_FOUND',
							type,
						};
					}

					if (type == 'city' && hasCity) {
						var ads = ad.filter((item) => item.cities.includes(city));

						if(ads.length == 0) {
							ads = ad.filter((item) => item.cities.length == 0);
						}

						// random ad
						if (ads.length > 0) {
							const randomAd = ads[Math.floor(Math.random() * ads.length)];

							return {
								status: 200,
								meta: {},
								ad: randomAd
							};
						}

						return {
							status: 404,
							message: 'AD_NOT_FOUND',
							type,
						};
					}

					return {
						status: 400,
						message: 'INVALID_TYPE'
					};
				} catch (error) {
					console.error(error);

					return {
						status: 500
					};
				}
			}
		}
	},
}